package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// mIntervalDays matches the "1 day " prefix pg_dump writes for an interval.
var mIntervalDays = regexp.MustCompile(`^(\d+)\s+days?\s+`)

// refSchema is the schema an app creates in its own SQLite database. A Postgres
// schema cannot express what those tables carry: column collations, STRICT and
// WITHOUT ROWID modifiers, CHECK constraints calling functions only the app
// registers, and timestamps it keeps as epoch integers.
type refSchema struct {
	tables map[string]*refTable
}

type refTable struct {
	types     map[string]string
	generated map[string]bool
}

// loadRefSchema reads the column types of a database the target app created.
func loadRefSchema(path string) (*refSchema, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("opening reference schema %s: %w", path, err)
	}

	defer func() { _ = db.Close() }()

	names, err := refTableNames(db)
	if err != nil {
		return nil, fmt.Errorf("reading schema of %s: %w", path, err)
	}

	ref := &refSchema{tables: make(map[string]*refTable, len(names))}

	for _, name := range names {
		types, generated, err := refColumnTypes(db, name)
		if err != nil {
			return nil, fmt.Errorf("reading schema of %s: %w", path, err)
		}

		ref.tables[name] = &refTable{types: types, generated: generated}
	}

	return ref, nil
}

// copyRefSchema writes the reference database to path, schema and all, so the
// output starts as the app's own database rather than a reconstruction of it.
// Copying the file beats re-creating the DDL, which SQLite refuses while a
// CHECK constraint names a function only the app registers.
func copyRefSchema(source, path string) error {
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return fmt.Errorf("opening reference schema %s: %w", source, err)
	}

	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()

		return fmt.Errorf("checkpointing reference schema %s: %w", source, err)
	}

	if err := db.Close(); err != nil {
		return fmt.Errorf("closing reference schema %s: %w", source, err)
	}

	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("reading reference schema %s: %w", source, err)
	}

	defer func() { _ = in.Close() }()

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return fmt.Errorf("copying reference schema to %s: %w", path, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

// flushOutput folds a write-ahead log back into the database and leaves it in
// the journal mode a fresh SQLite file has, so the finished file stands alone.
func flushOutput(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpointing the output database: %w", err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
		return fmt.Errorf("switching the output database out of wal mode: %w", err)
	}

	return nil
}

func refTableNames(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master
		WHERE type = 'table' AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var names []string

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}

		names = append(names, name)
	}

	return names, rows.Err()
}

func refColumnTypes(db *sql.DB, name string) (map[string]string, map[string]bool, error) {
	rows, err := db.Query(`SELECT name, type, hidden FROM pragma_table_xinfo(` + quoteIdent(name) + `)`)
	if err != nil {
		return nil, nil, err
	}

	defer func() { _ = rows.Close() }()

	types := map[string]string{}
	generated := map[string]bool{}

	for rows.Next() {
		var (
			colName, colType string
			hidden           int
		)

		if err := rows.Scan(&colName, &colType, &hidden); err != nil {
			return nil, nil, err
		}

		types[colName] = colType

		// table_info hides generated columns, which the app computes and an
		// insert must leave alone.
		if hidden != 0 {
			generated[colName] = true
		}
	}

	return types, generated, rows.Err()
}

// clearTables empties the reference database so the dump supplies every row.
// Its rows record the app's own migrations, which the dump also carries.
func clearTables(ctx context.Context, tx *sql.Tx, ref *refSchema) error {
	for name := range ref.tables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+quoteIdent(name)); err != nil {
			return fmt.Errorf("emptying table %s: %w", name, err)
		}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM sqlite_sequence"); err != nil &&
		!strings.Contains(err.Error(), "no such table") {
		return fmt.Errorf("resetting sqlite_sequence: %w", err)
	}

	return nil
}

// refResolver points every table a parse pass yields at its reference
// definition. Both passes build their own tables, so this runs per pass and
// warns only the first time it sees a name.
type refResolver struct {
	ref    *refSchema
	rep    *reporter
	seen   map[*table]bool
	warned map[string]bool
}

func newRefResolver(ref *refSchema, rep *reporter) *refResolver {
	return &refResolver{
		ref:    ref,
		rep:    rep,
		seen:   map[*table]bool{},
		warned: map[string]bool{},
	}
}

// apply points t at its reference definition. Columns the reference does not
// have keep their rows out of the insert, since the app's own schema decides
// what a table holds.
func (r *refResolver) apply(t *table) {
	if r.seen[t] {
		return
	}

	r.seen[t] = true

	rt, ok := r.ref.tables[t.name]
	if !ok {
		t.absent = true
		r.warn(t.name, "table is not in the reference schema, its rows are dropped")

		return
	}

	var dropped []string

	for i := range t.cols {
		if rt.generated[t.cols[i].name] {
			t.cols[i].missing = true

			continue
		}

		decl, ok := rt.types[t.cols[i].name]
		if !ok {
			t.cols[i].missing = true
			dropped = append(dropped, t.cols[i].name)

			continue
		}

		t.cols[i].sqlType = decl
	}

	if len(dropped) > 0 {
		r.warn(t.name, "columns are not in the reference schema, their values are dropped",
			"columns", summarise(dropped))
	}
}

func (r *refResolver) warn(table, msg string, args ...any) {
	if r.warned[table] {
		return
	}

	r.warned[table] = true

	r.rep.warn(msg, append([]any{"table", table}, args...)...)
}

// isIntervalType reports whether a PostgreSQL type holds a duration.
func isIntervalType(pgType string) bool {
	return strings.TrimSpace(strings.ToLower(pgType)) == "interval"
}

// intervalMillis reads the interval form pg_dump writes, such as "00:05:00" or
// "1 day 02:03:04.5", as the milliseconds an app stores in an integer column.
func intervalMillis(text string) (int64, bool) {
	s := strings.TrimSpace(text)

	neg := strings.HasPrefix(s, "-")
	s = strings.TrimSpace(strings.TrimPrefix(s, "-"))

	var total time.Duration

	if loc := mIntervalDays.FindStringSubmatch(s); loc != nil {
		days, err := strconv.Atoi(loc[1])
		if err != nil {
			return 0, false
		}

		total += time.Duration(days) * 24 * time.Hour

		s = strings.TrimSpace(s[len(loc[0]):])
	}

	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}

	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}

	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}

	seconds, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, false
	}

	total += time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute +
		time.Duration(seconds*float64(time.Second))

	if neg {
		total = -total
	}

	return total.Milliseconds(), true
}

// sqliteTimeLayout is the format the SQLite drivers write and parse for a time
// column; PostgreSQL's "+00" offset is not parseable back into time.Time.
const sqliteTimeLayout = "2006-01-02 15:04:05.999999999-07:00"

const dateLayout = "2006-01-02"

var plainTimestampLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

// isTimestampType reports whether a PostgreSQL type holds a date and time.
func isTimestampType(pgType string) bool {
	t := strings.ToLower(strings.TrimSpace(pgType))

	return strings.HasPrefix(t, "timestamp") || t == "timestamptz" || t == "date"
}

func isTimeDeclType(decl string) bool {
	switch strings.ToUpper(strings.TrimSpace(decl)) {
	case "DATETIME", "TIMESTAMP", "DATE":
		return true
	default:
		return false
	}
}

func isDateDeclType(decl string) bool {
	return strings.EqualFold(strings.TrimSpace(decl), "date")
}

// isDateType reports whether a PostgreSQL type holds a date without a time.
func isDateType(pgType string) bool {
	return strings.EqualFold(strings.TrimSpace(pgType), "date")
}

func isIntegerDeclType(decl string) bool {
	switch strings.ToUpper(strings.TrimSpace(decl)) {
	case "INTEGER", "INT", "TINYINT", "SMALLINT", "MEDIUMINT", "BIGINT",
		"INT2", "INT4", "INT8", "UNSIGNED BIG INT":
		return true
	default:
		return false
	}
}

func isBoolDeclType(decl string) bool {
	switch strings.ToUpper(strings.TrimSpace(decl)) {
	case "BOOLEAN", "BOOL":
		return true
	default:
		return false
	}
}

// parsePGTime reads the timestamp forms pg_dump writes, zoned or not.
func parsePGTime(text string) (time.Time, bool) {
	for _, layout := range timestampLayouts {
		if ts, err := time.Parse(layout, text); err == nil {
			return ts, true
		}
	}

	for _, layout := range plainTimestampLayouts {
		if ts, err := time.Parse(layout, text); err == nil {
			return ts, true
		}
	}

	if ts, err := time.Parse(dateLayout, text); err == nil {
		return ts, true
	}

	return time.Time{}, false
}

// toSQLiteTime renders a PostgreSQL timestamp so the driver reads it back as a
// time.Time. Unparseable values pass through for SQLite to reject.
func toSQLiteTime(text string) string {
	ts, ok := parsePGTime(text)
	if !ok {
		return text
	}

	return ts.Format(sqliteTimeLayout)
}

// toEpochMillis renders a PostgreSQL timestamp as the unix milliseconds an app
// stores in an integer column.
func toEpochMillis(text string) any {
	ts, ok := parsePGTime(text)
	if !ok {
		return text
	}

	return ts.UnixMilli()
}

// toSQLiteDate renders a PostgreSQL date as the date-only string a date column
// holds; Django's parse_date rejects anything with a time part.
func toSQLiteDate(text string) string {
	ts, ok := parsePGTime(text)
	if !ok {
		return text
	}

	return ts.Format(dateLayout)
}
