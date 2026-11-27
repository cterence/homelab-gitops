package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// tableCount reports how many rows landed in one table.
type tableCount struct {
	table string
	rows  int
}

// reporter logs each problem once and keeps count so --strict can refuse a
// conversion that needed guesses.
type reporter struct {
	count int
}

func (r *reporter) warn(msg string, args ...any) {
	r.count++

	slog.Warn(msg, args...)
}

// convertResult summarises one conversion.
type convertResult struct {
	tables         []tableCount
	rows           int
	indexes        int
	skippedIndexes int
	views          int
	skippedViews   int
	skipped        map[string]int
	warnings       int
}

// mapType maps a PostgreSQL type onto the SQLite storage class that holds it
// best: integers stay integers, everything textual stays text. Numerics become
// NUMERIC, which is a float, so digits beyond double precision round.
func mapType(pgType string) string {
	t := strings.ToLower(strings.TrimSpace(pgType))

	// SQLite has no array type; keep the literal form as text.
	if strings.HasSuffix(t, "[]") {
		return "TEXT"
	}

	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}

	if i := strings.Index(t, "("); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}

	switch t {
	case "smallint", "integer", "bigint", "int", "int2", "int4", "int8",
		"smallserial", "serial", "bigserial", "oid", "boolean", "bool",
		"bit", "varbit":
		return "INTEGER"
	case "real", "double precision", "float4", "float8":
		return "REAL"
	case "numeric", "decimal", "money":
		return "NUMERIC"
	case "bytea":
		return "BLOB"
	default:
		return "TEXT"
	}
}

// mapDefault rewrites a PostgreSQL DEFAULT expression into SQLite syntax, and
// reports false for defaults SQLite cannot reproduce (sequence lookups and
// client-side UUID generators belong to the old database, not the new one).
func mapDefault(expr string) (string, bool) {
	e := unwrapParens(strings.TrimSpace(expr))
	lower := strings.ToLower(e)

	switch {
	case e == "":
		return "", false
	case strings.HasPrefix(lower, "nextval("),
		strings.Contains(lower, "gen_random_uuid"),
		strings.Contains(lower, "uuid_generate"):
		return "", false
	}

	// now() AT TIME ZONE 'utc' is a timezone conversion of the current time.
	if head, _, ok := cutDepthZero(e, " at time zone "); ok {
		e = head
		lower = strings.ToLower(e)
	}

	switch lower {
	case "now()", "current_timestamp", "transaction_timestamp()",
		"statement_timestamp()", "clock_timestamp()", "localtimestamp":
		return "CURRENT_TIMESTAMP", true
	case "true":
		return "1", true
	case "false":
		return "0", true
	}

	// timezone('utc'::text, now()) is the current time expressed in a zone.
	if strings.HasPrefix(lower, "timezone(") && strings.Contains(lower, "now()") {
		return "CURRENT_TIMESTAMP", true
	}

	if i := depthZeroIndex(e, "::"); i >= 0 {
		e = strings.TrimSpace(e[:i])
	}

	// Any other function call is a PostgreSQL-only helper SQLite cannot run.
	if e == "" || isFunctionCall(e) {
		return "", false
	}

	return e, true
}

// isFunctionCall reports whether s is a bare call such as schema.fn(args).
func isFunctionCall(s string) bool {
	open := strings.IndexByte(s, '(')
	if open <= 0 || !strings.HasSuffix(s, ")") {
		return false
	}

	for _, c := range s[:open] {
		switch {
		case c == '_' || c == '.' || c == '"' || c >= '0' && c <= '9',
			c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}

	return true
}

// unwrapParens strips redundant parens such as ((0)) so the default can be
// matched against the known rewrites.
func unwrapParens(s string) string {
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		depth, wrapped := 0, true

		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i != len(s)-1 {
					wrapped = false
				}
			}
		}

		if !wrapped {
			return s
		}

		s = strings.TrimSpace(s[1 : len(s)-1])
	}

	return s
}

// cutDepthZero splits s at the first occurrence of sep outside parens and
// quotes.
func cutDepthZero(s, sep string) (head, tail string, ok bool) {
	i := depthZeroIndex(s, sep)
	if i < 0 {
		return s, "", false
	}

	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):]), true
}

func depthZeroIndex(s, sub string) int {
	depth := 0

	inQ := false

	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			inQ = !inQ
		case '(':
			if !inQ {
				depth++
			}
		case ')':
			if !inQ {
				depth--
			}
		}

		if depth == 0 && !inQ && i+len(sub) <= len(s) && strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}

	return -1
}

var (
	mAnyArray   = regexp.MustCompile(`(?i)\s*=\s*ANY\s*\(ARRAY\[(.*?)\]\)`)
	mJSONPath   = regexp.MustCompile(`([A-Za-z_][\w"]*)\s*#>>\s*'\{([^}]*)\}'`)
	mAtTimeZone = regexp.MustCompile(`(?i)\s+AT\s+TIME\s+ZONE\s+'[^']*'`)
)

// createTableSQL renders the CREATE TABLE statement. Checks can be dropped
// when SQLite rejects them, and constraints when the whole statement fails.
func createTableSQL(t *table, withChecks, withConstraints bool) string {
	defs := make([]string, 0, len(t.cols)+len(t.unique)+len(t.checks)+1)

	inlinePK := withConstraints && len(t.pk) == 1 && t.columnType(t.pk[0]) == "INTEGER"

	for _, col := range t.cols {
		def := "  " + quoteIdent(col.name) + " " + col.sqlType

		switch {
		case inlinePK && col.name == t.pk[0]:
			def += " PRIMARY KEY"
		case withConstraints && col.notNull:
			def += " NOT NULL"
		}

		if withConstraints && col.def != "" {
			def += " DEFAULT " + col.def
		}

		defs = append(defs, def)
	}

	if withConstraints {
		if len(t.pk) > 0 && !inlinePK {
			defs = append(defs, "  PRIMARY KEY ("+quotedList(t.pk)+")")
		}

		for _, cols := range t.unique {
			defs = append(defs, "  UNIQUE ("+quotedList(cols)+")")
		}

		if withChecks {
			for _, check := range t.checks {
				defs = append(defs, "  CHECK "+translatePredicate(stripCasts(check)))
			}
		}
	}

	return "CREATE TABLE " + quoteIdent(t.name) + " (\n" + strings.Join(defs, ",\n") + "\n)"
}

// createTable tries progressively simpler DDL so one unsupported constraint
// cannot lose a whole table. Every downgrade is reported.
func createTable(ctx context.Context, tx *sql.Tx, t *table, rep *reporter) error {
	variants := []struct {
		checks      bool
		constraints bool
		dropped     string
	}{
		{checks: true, constraints: true},
		{checks: false, constraints: true, dropped: "check constraints"},
		{checks: false, constraints: false, dropped: "constraints and defaults"},
	}

	var lastErr error

	for i, variant := range variants {
		_, err := tx.ExecContext(ctx, createTableSQL(t, variant.checks, variant.constraints))
		if err == nil {
			if i > 0 {
				rep.warn("table created with less than the source schema",
					"table", t.name, "dropped", variant.dropped)
			}

			return nil
		}

		lastErr = err
	}

	return fmt.Errorf("creating table %s: %w", t.name, lastErr)
}

func (t *table) columnType(name string) string {
	for _, col := range t.cols {
		if col.name == name {
			return col.sqlType
		}
	}

	return ""
}

func quotedList(names []string) string {
	quoted := make([]string, len(names))

	for i, name := range names {
		quoted[i] = quoteIdent(name)
	}

	return strings.Join(quoted, ", ")
}

// rowValues fills one row into values, converting per the target column types.
// The buffer is reused across rows to keep bulk loads allocation-light.
func rowValues(t *table, cols []*column, fields []string, values []any) ([]any, error) {
	if len(fields) != len(cols) {
		return nil, fmt.Errorf("table %s: row has %d fields, want %d", t.name, len(fields), len(cols))
	}

	if cap(values) < len(fields) {
		values = make([]any, len(fields))
	}

	values = values[:len(fields)]
	clear(values)

	for i, field := range fields {
		if field == `\N` {
			continue
		}

		col := cols[i]
		text := unescapeCopy(field)

		switch {
		case col.sqlType == "BLOB":
			blob, err := decodeBytea(text)
			if err != nil {
				return nil, fmt.Errorf("table %s column %s: %w", t.name, col.name, err)
			}

			values[i] = blob
		case col.sqlType == "INTEGER" && isBoolType(col.pgType):
			switch text {
			case "t", "true", "1":
				values[i] = int64(1)
			case "f", "false", "0":
				values[i] = int64(0)
			default:
				return nil, fmt.Errorf("table %s column %s: %q is not a boolean", t.name, col.name, text)
			}
		case isTimestampTZ(col.pgType):
			// Normalising to UTC keeps lexicographic order chronological even
			// when the source session rendered rows in different offsets.
			values[i] = normalizeTimestamp(text)
		default:
			values[i] = text
		}
	}

	return values, nil
}

func isBoolType(pgType string) bool {
	t := strings.ToLower(strings.TrimSpace(pgType))

	return t == "boolean" || t == "bool"
}

func isTimestampTZ(pgType string) bool {
	t := strings.ToLower(strings.TrimSpace(pgType))

	return t == "timestamptz" ||
		(strings.Contains(t, "timestamp") && strings.Contains(t, "with time zone"))
}

var timestampLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05-07",
}

// normalizeTimestamp renders a timestamptz value in UTC, matching PostgreSQL's
// own text form. Unparseable values pass through untouched.
func normalizeTimestamp(text string) string {
	for _, layout := range timestampLayouts {
		ts, err := time.Parse(layout, text)
		if err != nil {
			continue
		}

		_, offset := ts.Zone()
		if offset == 0 {
			return text
		}

		out := ts.UTC().Format("2006-01-02 15:04:05")
		if ns := ts.Nanosecond(); ns != 0 {
			out += strings.TrimRight(fmt.Sprintf(".%09d", ns), "0")
		}

		return out + "+00"
	}

	return text
}

// decodeBytea decodes PostgreSQL's hex (\x0102) and escape (\001\002) forms.
func decodeBytea(s string) ([]byte, error) {
	if digits, ok := strings.CutPrefix(s, `\x`); ok {
		blob, err := hex.DecodeString(digits)
		if err != nil {
			return nil, fmt.Errorf("decoding bytea %q: %w", s, err)
		}

		return blob, nil
	}

	blob := make([]byte, 0, len(s))

	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			blob = append(blob, s[i])

			continue
		}

		if i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			blob = append(blob, (s[i+1]-'0')<<6|(s[i+2]-'0')<<3|(s[i+3]-'0'))
			i += 3

			continue
		}

		if i+1 < len(s) && s[i+1] == '\\' {
			blob = append(blob, '\\')
			i++

			continue
		}

		return nil, fmt.Errorf("unsupported bytea encoding %q", s)
	}

	return blob, nil
}

func isOctal(c byte) bool {
	return c >= '0' && c <= '7'
}

// convert creates every table from the dump, then streams the rows into them in
// a second pass, and finally builds indexes and views. Problems are warned about
// so a single unsupported object cannot cost the whole conversion.
//
// Two passes are what keep memory flat: the first reads the schema only, the
// second hands one row at a time to the writer. The source must therefore be
// seekable, which the caller guarantees by spooling stdin to disk.
func convert(ctx context.Context, source io.ReadSeeker, db *sql.DB, strict bool) (*convertResult, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewinding dump: %w", err)
	}

	d, err := parseDump(source, nil)
	if err != nil {
		return nil, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("starting transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	rep := &reporter{}
	res := &convertResult{skipped: d.skipped}

	for _, t := range d.tables {
		if err := createTable(ctx, tx, t, rep); err != nil {
			return nil, err
		}

		var dropped []string

		for _, col := range t.cols {
			if col.generated {
				rep.warn("generated column is empty", "table", t.name, "column", col.name,
					"detail", "pg_dump exports no value for generated columns")
			}

			if col.droppedDefault != "" && !t.inlineIntegerPK(col.name) {
				dropped = append(dropped, col.name)
			}
		}

		if len(dropped) > 0 {
			rep.warn("defaults dropped", "table", t.name, "columns", summarise(dropped),
				"detail", "the app must supply these values")
		}
	}

	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewinding dump for rows: %w", err)
	}

	writer := newInserter(ctx, tx)

	if _, err := parseDump(source, writer.row); err != nil {
		return nil, err
	}

	if err := writer.close(); err != nil {
		return nil, fmt.Errorf("closing insert statements: %w", err)
	}

	for _, t := range d.tables {
		for _, index := range t.indexes {
			if _, err := tx.ExecContext(ctx, index); err != nil {
				rep.warn("skipping index", "table", t.name,
					"index", strings.Join(strings.Fields(index), " "), "error", err)

				res.skippedIndexes++

				continue
			}

			res.indexes++
		}
	}

	// pg_dump emits a view twice when it carries a _RETURN rule, the second
	// time as CREATE OR REPLACE. Only the first definition is needed.
	createdViews := map[string]bool{}

	for _, view := range d.views {
		name := viewName(view)
		if createdViews[name] {
			res.skippedViews++

			continue
		}

		createdViews[name] = true

		sqlText := translateView(view, d.schemas)
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			rep.warn("skipping view", "view", sqlText, "error", err)

			res.skippedViews++

			continue
		}

		res.views++
	}

	for _, t := range d.tables {
		rows := writer.counts[t.name]
		res.tables = append(res.tables, tableCount{table: t.name, rows: rows})
		res.rows += rows
	}

	res.warnings = rep.count

	if strict && rep.count > 0 {
		return nil, fmt.Errorf("%d warning(s) in strict mode, see the log above", rep.count)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}

	return res, nil
}

// viewName returns the SQLite name a view statement would create.
func viewName(stmt string) string {
	body := strings.TrimSpace(stmt)

	for _, prefix := range []string{"CREATE OR REPLACE VIEW ", "CREATE VIEW "} {
		if after, ok := strings.CutPrefix(body, prefix); ok {
			body = after

			break
		}
	}

	name, _ := nextToken(body)

	return sqliteTableName(splitTableRef(name))
}

// summarise lists a few names and counts the rest, to keep log lines short.
func summarise(names []string) string {
	const limit = 3

	if len(names) <= limit {
		return strings.Join(names, ",")
	}

	return strings.Join(names[:limit], ",") + ",… +" + strconv.Itoa(len(names)-limit)
}

// translateView rewrites a PostgreSQL view for SQLite: casts and schema
// qualifiers go. Anything still unsupported fails when it is created.
func translateView(stmt string, schemas map[string]bool) string {
	out := stripCasts(strings.TrimSuffix(strings.TrimSpace(stmt), ";"))
	out = strings.ReplaceAll(out, " OR REPLACE", "")

	for schema := range schemas {
		if schema == "" || schema == "public" {
			out = strings.ReplaceAll(out, "public.", "")
		} else {
			out = strings.ReplaceAll(out, schema+".", schema+"__")
		}
	}

	return out
}

func (t *table) inlineIntegerPK(name string) bool {
	return len(t.pk) == 1 && t.pk[0] == name && t.columnType(name) == "INTEGER"
}

// insertPlan is the prepared statement for one table and column order, with its
// columns resolved once.
type insertPlan struct {
	stmt *sql.Stmt
	cols []*column
}

// inserter writes rows as the parser hands them over. One prepared statement is
// kept per table and column list, so a dump of any size costs a fixed amount of
// memory.
type inserter struct {
	ctx     context.Context
	tx      *sql.Tx
	plans   map[string]*insertPlan
	counts  map[string]int
	values  []any
	seen    int
	lastKey string
	last    *insertPlan
}

func newInserter(ctx context.Context, tx *sql.Tx) *inserter {
	return &inserter{
		ctx:    ctx,
		tx:     tx,
		plans:  map[string]*insertPlan{},
		counts: map[string]int{},
	}
}

func (w *inserter) row(t *table, cols []string, fields []string) error {
	// Cancellation is honoured between rows; the statement itself is executed
	// without a context so the driver skips its per-call interrupt goroutine.
	if w.seen%1024 == 0 {
		if err := w.ctx.Err(); err != nil {
			return err
		}
	}

	plan, err := w.plan(t, cols)
	if err != nil {
		return err
	}

	if len(fields) != len(plan.cols) {
		return fmt.Errorf("table %s: row has %d fields, want %d", t.name, len(fields), len(plan.cols))
	}

	if w.values, err = rowValues(t, plan.cols, fields, w.values); err != nil {
		return err
	}

	if _, err := plan.stmt.Exec(w.values...); err != nil {
		return fmt.Errorf("inserting into table %s row %d: %w", t.name, w.counts[t.name]+1, err)
	}

	w.counts[t.name]++
	w.seen++

	return nil
}

// plan returns the prepared statement for a table and column list. Consecutive
// rows almost always share both, so the common path is a pointer comparison.
func (w *inserter) plan(t *table, cols []string) (*insertPlan, error) {
	key := t.name + "\x00" + strings.Join(cols, "\x00")
	if w.last != nil && key == w.lastKey {
		return w.last, nil
	}

	if plan, ok := w.plans[key]; ok {
		w.last, w.lastKey = plan, key

		return plan, nil
	}

	names := cols
	if len(names) == 0 {
		names = make([]string, len(t.cols))
		for i, col := range t.cols {
			names[i] = col.name
		}
	}

	quoted := make([]string, len(names))
	placeholders := make([]string, len(names))

	for i, name := range names {
		quoted[i] = quoteIdent(name)
		placeholders[i] = "?"
	}

	query := "INSERT INTO " + quoteIdent(t.name) + " (" + strings.Join(quoted, ", ") +
		") VALUES (" + strings.Join(placeholders, ", ") + ")"

	stmt, err := w.tx.PrepareContext(w.ctx, query)
	if err != nil {
		return nil, fmt.Errorf("preparing insert into table %s: %w", t.name, err)
	}

	plan := &insertPlan{stmt: stmt, cols: t.insertColumns(cols)}
	w.plans[key] = plan
	w.last, w.lastKey = plan, key

	return plan, nil
}

func (w *inserter) close() error {
	var err error

	for key, plan := range w.plans {
		if closeErr := plan.stmt.Close(); closeErr != nil && err == nil {
			err = closeErr
		}

		delete(w.plans, key)
	}

	return err
}
