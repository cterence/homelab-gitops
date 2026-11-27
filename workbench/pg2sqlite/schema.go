package main

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// mIntervalDays matches the "1 day " prefix pg_dump writes for an interval.
var mIntervalDays = regexp.MustCompile(`^(\d+)\s+days?\s+`)

// refSchema is the schema an app creates in its own SQLite database. A Postgres
// schema cannot express what those tables carry: column collations, STRICT and
// WITHOUT ROWID modifiers, and timestamps an app keeps as epoch integers.
type refSchema struct {
	tables  map[string]*refTable
	objects []refObject
}

type refTable struct {
	ddl   string
	types map[string]string
}

// refObject is an index, trigger or view belonging to the reference schema.
type refObject struct {
	kind string
	sql  string
}

// loadRefSchema reads the schema of a SQLite database the target app created.
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
		ddl, types, err := refTableDefinition(db, name)
		if err != nil {
			return nil, fmt.Errorf("reading schema of %s: %w", path, err)
		}

		ref.tables[name] = &refTable{ddl: ddl, types: types}
	}

	if ref.objects, err = refObjects(db); err != nil {
		return nil, fmt.Errorf("reading schema of %s: %w", path, err)
	}

	return ref, nil
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

func refTableDefinition(db *sql.DB, name string) (string, map[string]string, error) {
	var ddl string

	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&ddl)
	if err != nil {
		return "", nil, err
	}

	rows, err := db.Query(`PRAGMA table_info(` + quoteIdent(name) + `)`)
	if err != nil {
		return "", nil, err
	}

	defer func() { _ = rows.Close() }()

	types := map[string]string{}

	for rows.Next() {
		var (
			cid, notNull, pk int
			colName, colType string
			def              sql.NullString
		)

		if err := rows.Scan(&cid, &colName, &colType, &notNull, &def, &pk); err != nil {
			return "", nil, err
		}

		types[colName] = colType
	}

	return ddl, types, rows.Err()
}

func refObjects(db *sql.DB) ([]refObject, error) {
	rows, err := db.Query(`SELECT type, sql FROM sqlite_master
		WHERE type IN ('index', 'trigger', 'view') AND sql IS NOT NULL
		ORDER BY CASE type WHEN 'index' THEN 0 WHEN 'trigger' THEN 1 ELSE 2 END, name`)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var objects []refObject

	for rows.Next() {
		var object refObject
		if err := rows.Scan(&object.kind, &object.sql); err != nil {
			return nil, err
		}

		objects = append(objects, object)
	}

	return objects, rows.Err()
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

	t.ddl = rt.ddl

	var dropped []string

	for i := range t.cols {
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
