package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// rowHandler receives every row as it is parsed, so the caller can write it
// straight to the database instead of the parser holding it in memory.
type rowHandler func(t *table, cols []string, row []string) error

// table is one CREATE TABLE plus the constraints and indexes the dump emits for
// it elsewhere in the file.
type table struct {
	name    string
	schema  string
	pgName  string
	cols    []column
	pk      []string
	unique  [][]string
	checks  []string
	indexes []string
}

type column struct {
	name           string
	pgType         string
	sqlType        string
	def            string
	droppedDefault string
	notNull        bool
	generated      bool
}

// dump is everything a plain-format pg_dump file yields.
type dump struct {
	tables  []*table
	views   []string
	schemas map[string]bool
	skipped map[string]int
	byName  map[string]*table
	parts   map[string]*table
}

// stopKeywords end the type part of a column definition.
var stopKeywords = []string{
	"NOT", "NULL", "DEFAULT", "PRIMARY", "UNIQUE",
	"REFERENCES", "CHECK", "GENERATED", "COLLATE", "CONSTRAINT",
}

var createTablePrefixes = []string{"CREATE UNLOGGED TABLE ", "CREATE FOREIGN TABLE ", "CREATE TABLE "}

// parseDump reads a plain-format (pg_dump without -Fc) dump. Statements that
// carry no table data are counted so the caller can report what was skipped.
// When rows is nil only the schema is collected; otherwise every row is handed
// to it as it is read, which keeps memory flat regardless of dump size.
func parseDump(r io.Reader, rows rowHandler) (*dump, error) {
	d := &dump{
		schemas: map[string]bool{},
		skipped: map[string]int{},
		byName:  map[string]*table{},
		parts:   map[string]*table{},
	}

	reader := bufio.NewReaderSize(r, 1<<16)

	var (
		stmt      strings.Builder
		cur       *table
		curCols   []string
		copyState bool
	)

	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("reading dump: %w", readErr)
		}

		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")

		// EOF with nothing left over is the end of the file, not an empty line:
		// feeding it to the COPY handler would invent a row.
		if line == "" && errors.Is(readErr, io.EOF) {
			break
		}

		if copyState {
			if line == `\.` {
				copyState, cur, curCols = false, nil, nil
			} else if rows != nil {
				values := strings.Split(line, "\t")
				if len(values) != len(curCols) {
					return nil, fmt.Errorf("COPY %s: row has %d values for %d columns", cur.name, len(values), len(curCols))
				}

				if err := rows(cur, curCols, values); err != nil {
					return nil, err
				}
			}
		} else if trimmed := strings.TrimSpace(line); trimmed != "" &&
			!strings.HasPrefix(trimmed, "--") && !strings.HasPrefix(trimmed, `\`) {
			stmt.WriteString(trimmed)
			stmt.WriteString("\n")

			if strings.HasSuffix(trimmed, ";") && dollarQuoteTag(stmt.String()) == "" {
				text := collapseSpace(stmt.String())

				stmt.Reset()

				startsCopy, copyTable, copyCols, stmtErr := d.statement(text, rows)
				if stmtErr != nil {
					return nil, stmtErr
				}

				if startsCopy {
					copyState, cur, curCols = true, copyTable, copyCols
				}
			}
		}

		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	// A dump cut short mid-COPY or mid-statement must never pass as complete:
	// the caller would commit a partially loaded database.
	if copyState {
		return nil, fmt.Errorf("truncated dump: the COPY block for %s has no terminator", cur.name)
	}

	if strings.TrimSpace(stmt.String()) != "" {
		return nil, fmt.Errorf("truncated dump: unterminated statement %q", firstLine(stmt.String()))
	}

	if len(d.tables) == 0 {
		return nil, errors.New("no CREATE TABLE statements found; is this a plain-format pg_dump file?")
	}

	return d, nil
}

// statement consumes one complete SQL statement. It reports whether the
// statement opens a COPY block, and which table and columns that block feeds.
func (d *dump) statement(text string, rows rowHandler) (startsCopy bool, t *table, cols []string, err error) {
	upper := strings.ToUpper(text)

	switch {
	case hasAnyPrefix(upper, createTablePrefixes...):
		return false, nil, nil, d.createTable(text)
	case strings.HasPrefix(upper, "CREATE UNIQUE INDEX"), strings.HasPrefix(upper, "CREATE INDEX"):
		d.index(text)
	case strings.HasPrefix(upper, "ALTER TABLE"):
		d.alterTable(text)
	case strings.HasPrefix(upper, "COPY "):
		return d.copyStart(text)
	case strings.HasPrefix(upper, "INSERT INTO "):
		return false, nil, nil, d.insert(text, rows)
	case strings.HasPrefix(upper, "CREATE VIEW"), strings.HasPrefix(upper, "CREATE OR REPLACE VIEW"):
		d.views = append(d.views, text)
	case strings.HasPrefix(upper, "CREATE MATERIALIZED VIEW"):
		d.skip("materialized view")
	case strings.HasPrefix(upper, "CREATE SEQUENCE"), strings.HasPrefix(upper, "ALTER SEQUENCE"):
		d.skip("sequence")
	case strings.HasPrefix(upper, "CREATE TYPE"), strings.HasPrefix(upper, "CREATE DOMAIN"):
		d.skip("type")
	case strings.HasPrefix(upper, "CREATE FUNCTION"), strings.HasPrefix(upper, "CREATE PROCEDURE"),
		strings.HasPrefix(upper, "CREATE AGGREGATE"):
		d.skip("function")
	case strings.HasPrefix(upper, "CREATE TRIGGER"):
		d.skip("trigger")
	}

	return false, nil, nil, nil
}

func (d *dump) skip(kind string) {
	d.skipped[kind]++
}

func (d *dump) createTable(text string) error {
	rest := text
	for _, prefix := range createTablePrefixes {
		if after, ok := strings.CutPrefix(rest, prefix); ok {
			rest = after

			break
		}
	}

	rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "IF NOT EXISTS "))

	// A partition child has no column list: its rows belong to the parent.
	if idx := indexWordFold(rest, " PARTITION OF "); idx >= 0 {
		parentRef := rest[idx+len(" PARTITION OF "):]
		if v := indexWordFold(parentRef, " FOR VALUES"); v >= 0 {
			parentRef = parentRef[:v]
		}

		parent := d.resolve(parentRef)
		if parent == nil {
			return fmt.Errorf("partition %q of unknown table %q", strings.TrimSpace(rest[:idx]), strings.TrimSpace(parentRef))
		}

		d.parts[sqliteTableName(splitTableRef(rest[:idx]))] = parent

		return nil
	}

	open := strings.Index(rest, "(")
	if open < 0 {
		return fmt.Errorf("no column list in %q", firstLine(text))
	}

	closeIdx := matchParen(rest, open)
	if closeIdx < 0 {
		return fmt.Errorf("unbalanced column list in %q", firstLine(text))
	}

	schema, name := splitTableRef(rest[:open])
	if name == "" {
		return fmt.Errorf("no table name in %q", firstLine(text))
	}

	t := &table{name: sqliteTableName(schema, name), schema: schema, pgName: name}

	for _, item := range splitTopLevel(rest[open+1:closeIdx], ',') {
		if item == "" {
			continue
		}

		if after, ok := strings.CutPrefix(item, "CONSTRAINT "); ok {
			_, item = nextToken(after)

			d.tableConstraint(t, item)

			continue
		}

		if isTableConstraint(item) {
			d.tableConstraint(t, item)

			continue
		}

		col, err := parseColumn(item)
		if err != nil {
			return fmt.Errorf("table %s: %w", t.name, err)
		}

		t.cols = append(t.cols, col)
	}

	if len(t.cols) == 0 {
		return fmt.Errorf("table %s has no columns", t.name)
	}

	if d.byName[t.name] != nil {
		return fmt.Errorf("two tables map to the SQLite name %q", t.name)
	}

	d.tables = append(d.tables, t)
	d.byName[t.name] = t
	d.schemas[schema] = true

	return nil
}

func isTableConstraint(item string) bool {
	upper := strings.ToUpper(item)

	return hasPrefixWord(upper, "PRIMARY KEY", "UNIQUE", "FOREIGN KEY", "CHECK", "EXCLUDE")
}

func (d *dump) tableConstraint(t *table, body string) {
	upper := strings.ToUpper(body)

	switch {
	case hasPrefixWord(upper, "PRIMARY KEY"):
		t.pk = parenColumns(body)
	case hasPrefixWord(upper, "UNIQUE"):
		if cols := parenColumns(body); len(cols) > 0 {
			t.unique = append(t.unique, cols)
		}
	case hasPrefixWord(upper, "CHECK"):
		if group, ok := parenGroup(body); ok {
			t.checks = append(t.checks, group)
		}
	case hasPrefixWord(upper, "FOREIGN KEY"), hasPrefixWord(upper, "EXCLUDE"):
		d.skip("foreign key constraint")
	}
}

func (d *dump) alterTable(text string) {
	ref, kind, cols, ok := parseAddConstraint(text)
	if !ok {
		if strings.Contains(strings.ToUpper(text), "FOREIGN KEY") {
			d.skip("foreign key constraint")
		}

		return
	}

	t := d.resolve(ref)
	if t == nil {
		d.skip("constraint on unknown table")

		return
	}

	d.applyConstraint(t, kind, cols)
}

func (d *dump) applyConstraint(t *table, kind string, cols []string) {
	switch kind {
	case "primary":
		t.pk = cols
	case "unique":
		if len(cols) > 0 {
			t.unique = append(t.unique, cols)
		}
	case "check":
		if len(cols) > 0 {
			t.checks = append(t.checks, cols[0])
		}
	}
}

// parseAddConstraint splits "ALTER TABLE ... ADD CONSTRAINT ..." into the table
// reference, the constraint kind and its columns or expression.
func parseAddConstraint(stmt string) (tableRef, kind string, cols []string, ok bool) {
	body := strings.TrimPrefix(strings.TrimSpace(stmt), "ALTER TABLE ")
	body = strings.TrimPrefix(body, "ONLY ")

	idx := indexWordFold(body, " ADD CONSTRAINT ")
	if idx < 0 {
		return "", "", nil, false
	}

	tableRef = strings.TrimSpace(body[:idx])

	_, rest := nextToken(strings.TrimSpace(body[idx+len(" ADD CONSTRAINT "):]))

	upper := strings.ToUpper(rest)

	switch {
	case hasPrefixWord(upper, "PRIMARY KEY"):
		kind, cols = "primary", parenColumns(rest)
	case hasPrefixWord(upper, "UNIQUE"):
		kind, cols = "unique", parenColumns(rest)
	case hasPrefixWord(upper, "CHECK"):
		kind = "check"

		if group, groupOK := parenGroup(rest); groupOK {
			cols = []string{group}
		}
	default:
		return "", "", nil, false
	}

	if len(cols) == 0 {
		return "", "", nil, false
	}

	return tableRef, kind, cols, true
}

func (d *dump) index(text string) {
	ref, rewritten, ok := rewriteIndex(text, func(ref string) string {
		if t := d.resolve(ref); t != nil {
			return t.name
		}

		return ""
	})

	if !ok {
		d.skip("index")

		return
	}

	t := d.resolve(ref)
	if t == nil {
		d.skip("index on unknown table")

		return
	}

	t.indexes = append(t.indexes, rewritten)
}

func (d *dump) copyStart(text string) (startsCopy bool, t *table, cols []string, err error) {
	ref, cols, ok := parseCopyHeader(text)
	if !ok {
		return false, nil, nil, nil
	}

	t = d.resolve(ref)
	if t == nil {
		return false, nil, nil, fmt.Errorf("COPY into unknown table %q", ref)
	}

	// COPY without a column list means every non-generated column, in order.
	if len(cols) == 0 {
		cols = defaultColumns(t)
	}

	for _, col := range cols {
		if t.column(col) == nil {
			return false, nil, nil, fmt.Errorf("COPY %s references unknown column %q", ref, col)
		}
	}

	return true, t, cols, nil
}

func (d *dump) insert(text string, rows rowHandler) error {
	ref, cols, tuples, err := parseInsert(text)
	if err != nil {
		return err
	}

	t := d.resolve(ref)
	if t == nil {
		return fmt.Errorf("INSERT into unknown table %q", ref)
	}

	if len(cols) == 0 {
		cols = defaultColumns(t)
	}

	for _, col := range cols {
		if t.column(col) == nil {
			return fmt.Errorf("INSERT into %s references unknown column %q", ref, col)
		}
	}

	if rows == nil {
		return nil
	}

	for _, tuple := range tuples {
		if len(tuple) != len(cols) {
			return fmt.Errorf("INSERT into %s: row has %d values for %d columns", ref, len(tuple), len(cols))
		}

		if err := rows(t, cols, tuple); err != nil {
			return err
		}
	}

	return nil
}

// resolve maps a dump table reference to its SQLite table, folding partition
// children into their parent.
func (d *dump) resolve(ref string) *table {
	key := sqliteTableName(splitTableRef(ref))
	if parent, ok := d.parts[key]; ok {
		return parent
	}

	return d.byName[key]
}

func splitTableRef(ref string) (schema, name string) {
	ref = strings.TrimSpace(ref)

	dot := -1

	inQ := false

	for i := 0; i < len(ref); i++ {
		switch ref[i] {
		case '"':
			inQ = !inQ
		case '.':
			if !inQ {
				dot = i
			}
		}
	}

	if dot < 0 {
		return "", unquoteIdentifier(ref)
	}

	return unquoteIdentifier(ref[:dot]), unquoteIdentifier(ref[dot+1:])
}

// sqliteTableName flattens a schema-qualified name into one namespace, so two
// schemas holding a table of the same name stay distinct.
func sqliteTableName(schema, name string) string {
	if schema == "" || schema == "public" {
		return name
	}

	return schema + "__" + name
}

// column returns the named column, or nil when the table has no such column.
func (t *table) column(name string) *column {
	for i, col := range t.cols {
		if col.name == name {
			return &t.cols[i]
		}
	}

	return nil
}

// defaultColumns is the column list a COPY or INSERT without one covers: every
// non-generated column, in table order.
func defaultColumns(t *table) []string {
	names := make([]string, 0, len(t.cols))
	for _, col := range t.cols {
		if !col.generated {
			names = append(names, col.name)
		}
	}

	return names
}

// insertColumns is the column order one block of rows is stored in. An empty
// column list means the whole table, in table order.
func (t *table) insertColumns(names []string) []*column {
	if len(names) == 0 {
		cols := make([]*column, len(t.cols))
		for i := range t.cols {
			cols[i] = &t.cols[i]
		}

		return cols
	}

	cols := make([]*column, len(names))
	for i, name := range names {
		cols[i] = t.column(name)
	}

	return cols
}

// dollarQuoteTag returns the dollar-quote tag left open by s, or "" when every
// quote is closed. Handles $$ and $tag$ function bodies.
func dollarQuoteTag(s string) string {
	open := ""
	inQ := false

	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			inQ = !inQ
		case '$':
			if inQ {
				continue
			}

			j := i + 1
			for j < len(s) && (s[j] == '_' || isAlphaNum(s[j])) {
				j++
			}

			if j < len(s) && s[j] == '$' {
				if tag := s[i : j+1]; open == "" {
					open = tag
				} else if open == tag {
					open = ""
				}

				i = j
			}
		}
	}

	return open
}

func parseColumn(item string) (column, error) {
	tokens := scanTokens(item)
	if len(tokens) == 0 {
		return column{}, errors.New("empty column definition")
	}

	col := column{name: unquoteIdentifier(tokens[0].text)}
	if len(tokens) < 2 {
		return column{}, fmt.Errorf("column %s has no type", col.name)
	}

	typeEnd := len(tokens)
	for i := 1; i < len(tokens); i++ {
		if isStopKeyword(tokens[i].text) {
			typeEnd = i

			break
		}
	}

	if typeEnd == 1 {
		return column{}, fmt.Errorf("column %s has no type", col.name)
	}

	col.pgType = strings.TrimSpace(item[tokens[1].start:tokens[typeEnd-1].end])
	col.sqlType = mapType(col.pgType)

	if typeEnd < len(tokens) {
		rest := item[tokens[typeEnd].start:]
		upper := strings.ToUpper(rest)
		col.notNull = strings.Contains(upper, "NOT NULL")
		col.generated = strings.Contains(upper, "GENERATED ALWAYS AS")

		if expr, ok := defaultExpr(item, tokens, typeEnd); ok {
			if mapped, keep := mapDefault(expr); keep {
				col.def = mapped
			} else {
				col.droppedDefault = expr
			}
		}
	}

	return col, nil
}

// defaultExpr returns the raw DEFAULT expression that follows tokens[from],
// ending at the next stop keyword, so quoted spacing is preserved.
func defaultExpr(item string, tokens []token, from int) (string, bool) {
	for i := from; i < len(tokens); i++ {
		if !strings.EqualFold(tokens[i].text, "DEFAULT") {
			continue
		}

		end := len(item)
		for j := i + 1; j < len(tokens); j++ {
			if isStopKeyword(tokens[j].text) {
				end = tokens[j].start

				break
			}
		}

		return strings.TrimSpace(item[tokens[i].end:end]), true
	}

	return "", false
}

// rewriteIndex turns a PostgreSQL CREATE INDEX into SQLite syntax: the schema
// qualifier and USING method go, casts are dropped and the predicates SQLite
// spells differently are rewritten. Indexes SQLite cannot express are skipped.
// resolveName maps a dump table reference to its SQLite table name.
func rewriteIndex(stmt string, resolveName func(string) string) (tableRef, rewritten string, ok bool) {
	body := strings.TrimSuffix(strings.TrimSpace(stmt), ";")

	unique := false

	switch {
	case strings.HasPrefix(body, "CREATE UNIQUE INDEX "):
		unique = true
		body = strings.TrimPrefix(body, "CREATE UNIQUE INDEX ")
	case strings.HasPrefix(body, "CREATE INDEX "):
		body = strings.TrimPrefix(body, "CREATE INDEX ")
	default:
		return "", "", false
	}

	body = strings.TrimPrefix(body, "IF NOT EXISTS ")
	body = strings.TrimPrefix(body, "CONCURRENTLY ")

	name, rest := nextToken(body)
	if name == "" || !strings.HasPrefix(strings.ToUpper(rest), "ON ") {
		return "", "", false
	}

	rest = strings.TrimSpace(rest[len("ON "):])
	rest = strings.TrimPrefix(rest, "ONLY ")

	// Drop the USING clause but keep the table name and any WHERE predicate.
	if using := indexWordFold(rest, " USING "); using >= 0 {
		method, after := nextToken(strings.TrimSpace(rest[using+len(" USING "):]))
		if !strings.EqualFold(method, "btree") {
			return "", "", false
		}

		rest = rest[:using] + " " + after
	}

	target, after := nextToken(rest)

	open := strings.Index(after, "(")
	if open < 0 {
		return "", "", false
	}

	closeIdx := matchParen(after, open)
	if closeIdx < 0 {
		return "", "", false
	}

	cols := splitTopLevel(after[open+1:closeIdx], ',')
	for i, col := range cols {
		cols[i] = normalizeIndexColumn(col)
	}

	kind := "CREATE INDEX "
	if unique {
		kind = "CREATE UNIQUE INDEX "
	}

	table := resolveName(target)
	if table == "" {
		return "", "", false
	}

	rewritten = translatePredicate(stripCasts(kind + normalizeIdent(name) + " ON " + quoteIdent(table) +
		" (" + strings.Join(cols, ", ") + ")" + after[closeIdx+1:] + ";"))

	return target, rewritten, true
}

// translatePredicate rewrites the PostgreSQL spellings SQLite can express
// natively and gives up on the ones it cannot.
func translatePredicate(s string) string {
	s = mAnyArray.ReplaceAllString(s, " IN ($1)")
	s = mJSONPath.ReplaceAllStringFunc(s, func(match string) string {
		groups := mJSONPath.FindStringSubmatch(match)
		return "json_extract(" + groups[1] + ", '$." + strings.ReplaceAll(groups[2], ",", ".") + "')"
	})
	s = mAtTimeZone.ReplaceAllString(s, "")

	return strings.Join(strings.Fields(s), " ")
}

// stripCasts removes the ::type annotations PostgreSQL uses inside index
// expressions and predicates. SQLite is dynamically typed and rejects them.
func stripCasts(s string) string {
	if !strings.Contains(s, "::") {
		return s
	}

	var b strings.Builder

	b.Grow(len(s))

	inQ := false

	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			inQ = !inQ

			b.WriteByte(s[i])
		case !inQ && s[i] == ':' && i+1 < len(s) && s[i+1] == ':':
			j := i + 2
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}

			i = skipTypeName(s, j) - 1
		default:
			b.WriteByte(s[i])
		}
	}

	return strings.Join(strings.Fields(b.String()), " ")
}

// skipTypeName returns the index just past a type name starting at i,
// consuming multi-word names (character varying, timestamp with time zone)
// and array suffixes.
func skipTypeName(s string, i int) int {
	for {
		start := i
		for i < len(s) && isTypeByte(s[i]) {
			i++
		}

		if i == start {
			return i
		}

		for i+1 < len(s) && s[i] == '[' && s[i+1] == ']' {
			i += 2
		}

		switch rest := strings.ToLower(s[i:]); {
		case strings.HasPrefix(rest, " varying"), strings.HasPrefix(rest, " precision"):
			i++
		case strings.HasPrefix(rest, " with time zone"):
			i += len(" with time zone")
		case strings.HasPrefix(rest, " without time zone"):
			i += len(" without time zone")
		default:
			return i
		}
	}
}

func isTypeByte(c byte) bool {
	return c == '_' || c == '.' || c == '"' ||
		c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isAlphaNum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func parseCopyHeader(stmt string) (tableRef string, cols []string, ok bool) {
	body := strings.TrimSuffix(strings.TrimSpace(stmt), ";")
	if !strings.HasPrefix(body, "COPY ") || !strings.Contains(strings.ToUpper(body), " FROM STDIN") {
		return "", nil, false
	}

	rest := strings.TrimPrefix(body, "COPY ")

	if open := strings.Index(rest, "("); open >= 0 {
		if closeIdx := matchParen(rest, open); closeIdx > open {
			for _, col := range splitTopLevel(rest[open+1:closeIdx], ',') {
				cols = append(cols, unquoteIdentifier(col))
			}
		}
	}

	name := rest
	if open := strings.Index(rest, "("); open >= 0 {
		name = rest[:open]
	}

	name = strings.TrimSpace(name)
	if v := indexWordFold(name, " FROM"); v >= 0 {
		name = name[:v]
	}

	return strings.TrimSpace(name), cols, true
}

// parseInsert reads INSERT INTO ... VALUES (...), (...) and returns the rows in
// COPY text format, so both dump styles share one conversion path.
func parseInsert(stmt string) (tableRef string, cols []string, rows [][]string, err error) {
	body := strings.TrimSuffix(strings.TrimSpace(stmt), ";")
	body = strings.TrimPrefix(body, "INSERT INTO ")
	body = strings.TrimPrefix(body, "INTO ")

	idx := indexWordFold(body, " VALUES")
	if idx < 0 {
		return "", nil, nil, fmt.Errorf("INSERT without VALUES: %w", errUnsupported)
	}

	head := strings.TrimSpace(body[:idx])
	values := strings.TrimSpace(body[idx+len(" VALUES"):])

	if open := strings.Index(head, "("); open >= 0 {
		closeIdx := matchParen(head, open)
		if closeIdx < 0 {
			return "", nil, nil, fmt.Errorf("unbalanced column list in INSERT: %w", errUnsupported)
		}

		for _, col := range splitTopLevel(head[open+1:closeIdx], ',') {
			cols = append(cols, unquoteIdentifier(col))
		}

		head = head[:open]
	}

	rows, err = parseValueRows(values)
	if err != nil {
		return "", nil, nil, err
	}

	return strings.TrimSpace(head), cols, rows, nil
}

var errUnsupported = errors.New("unsupported dump statement")

// parseValueRows parses "(...), (...)" into rows of COPY-format fields.
func parseValueRows(s string) ([][]string, error) {
	var rows [][]string

	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', ',':
			continue
		case '(':
			fields, next, err := parseValueTuple(s, i)
			if err != nil {
				return nil, err
			}

			rows = append(rows, fields)
			i = next
		default:
			return nil, fmt.Errorf("unexpected %q in VALUES: %w", s[i], errUnsupported)
		}
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("no rows in VALUES: %w", errUnsupported)
	}

	return rows, nil
}

func parseValueTuple(s string, open int) ([]string, int, error) {
	var fields []string

	for i := open + 1; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', ',':
			continue
		case ')':
			return fields, i, nil
		}

		field, next, err := parseValue(s, i)
		if err != nil {
			return nil, 0, err
		}

		fields = append(fields, field)
		i = next - 1
	}

	return nil, 0, fmt.Errorf("unterminated VALUES tuple: %w", errUnsupported)
}

// parseValue converts one SQL literal into a COPY-format field. Values are
// re-escaped rather than bypassing the COPY decoder, so a single conversion
// path handles both dump styles.
func parseValue(s string, i int) (string, int, error) {
	if i >= len(s) {
		return "", 0, fmt.Errorf("unexpected end of VALUES: %w", errUnsupported)
	}

	// E'...' uses backslash escapes, '...' only doubles its quotes.
	if (s[i] == 'E' || s[i] == 'e') && i+1 < len(s) && s[i+1] == '\'' {
		text, next, err := readQuoted(s, i+1)
		if err != nil {
			return "", 0, err
		}

		return escapeCopyField(unescapeCopy(text)), next, nil
	}

	if s[i] == '\'' {
		text, next, err := readQuoted(s, i)
		if err != nil {
			return "", 0, err
		}

		return escapeCopyField(text), next, nil
	}

	start := i
	depth := 0

	for ; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				field, err := insertField(s[start:i])

				return field, i, err
			}

			depth--
		case ',':
			if depth == 0 {
				field, err := insertField(s[start:i])

				return field, i, err
			}
		case '\'':
			// A quoted section inside an unquoted token: skip it whole.
			_, next, err := readQuoted(s, i)
			if err != nil {
				return "", 0, err
			}

			i = next - 1
		}
	}

	return "", 0, fmt.Errorf("unterminated value: %w", errUnsupported)
}

func insertField(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("empty value: %w", errUnsupported)
	}

	switch strings.ToUpper(token) {
	case "NULL":
		return `\N`, nil
	case "TRUE":
		return "t", nil
	case "FALSE":
		return "f", nil
	}

	if strings.ContainsAny(token, "()") {
		return "", fmt.Errorf("unsupported expression %q: %w", token, errUnsupported)
	}

	return escapeCopyField(stripCasts(token)), nil
}

// readQuoted reads a single-quoted literal starting at open and returns its
// decoded contents plus the index just past the closing quote.
func readQuoted(s string, open int) (string, int, error) {
	var b strings.Builder

	for i := open + 1; i < len(s); i++ {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')

				i++

				continue
			}

			return b.String(), i + 1, nil
		}

		b.WriteByte(s[i])
	}

	return "", 0, fmt.Errorf("unterminated string: %w", errUnsupported)
}

// escapeCopyField re-escapes text into COPY format, the inverse of unescapeCopy
// for the characters that carry meaning there.
func escapeCopyField(s string) string {
	if !strings.ContainsAny(s, "\\\n\t\r") {
		return s
	}

	return copyEscaper.Replace(s)
}

var copyEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", `\r`)

func splitTopLevel(s string, sep rune) []string {
	var (
		parts []string
		cur   strings.Builder
		depth int
		inQ   bool
	)

	for i := 0; i < len(s); i++ {
		c := rune(s[i])

		switch {
		case c == '\'':
			inQ = !inQ

			cur.WriteByte(s[i])
		case inQ, c == '(' || c == ')':
			if c == '(' && !inQ {
				depth++
			}

			if c == ')' && !inQ {
				depth--
			}

			cur.WriteByte(s[i])
		case c == sep && depth == 0:
			if part := strings.TrimSpace(cur.String()); part != "" {
				parts = append(parts, part)
			}

			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}

	if part := strings.TrimSpace(cur.String()); part != "" {
		parts = append(parts, part)
	}

	return parts
}

type token struct {
	text  string
	start int
	end   int
}

func scanTokens(s string) []token {
	var (
		tokens []token
		depth  int
		inQ    bool
		start  = -1
	)

	flush := func(end int) {
		if start >= 0 {
			tokens = append(tokens, token{text: s[start:end], start: start, end: end})
			start = -1
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]

		switch {
		case c == '\'':
			inQ = !inQ

			if start < 0 {
				start = i
			}
		case c == '(':
			depth++

			if start < 0 {
				start = i
			}
		case c == ')':
			depth--

			if start < 0 {
				start = i
			}
		case inQ, depth > 0:
			if start < 0 {
				start = i
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush(i)
		default:
			if start < 0 {
				start = i
			}
		}
	}

	flush(len(s))

	return tokens
}

func nextToken(s string) (tok, rest string) {
	tokens := scanTokens(s)
	if len(tokens) == 0 {
		return "", ""
	}

	return tokens[0].text, strings.TrimSpace(s[tokens[0].end:])
}

// matchParen returns the index of the paren closing the one at open, skipping
// quoted sections, or -1 when it is unbalanced.
func matchParen(s string, open int) int {
	depth := 0

	inQ := false

	for i := open; i < len(s); i++ {
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
				if depth == 0 {
					return i
				}
			}
		}
	}

	return -1
}

// parenGroup returns the text of the first parenthesised group including its
// parens, e.g. "CHECK ((a > 0))" yields "((a > 0))".
func parenGroup(s string) (string, bool) {
	open := strings.Index(s, "(")
	if open < 0 {
		return "", false
	}

	closeIdx := matchParen(s, open)
	if closeIdx < 0 {
		return "", false
	}

	return s[open : closeIdx+1], true
}

func parenColumns(s string) []string {
	group, ok := parenGroup(s)
	if !ok {
		return nil
	}

	cols := splitTopLevel(group[1:len(group)-1], ',')
	for i, col := range cols {
		cols[i] = unquoteIdentifier(col)
	}

	return cols
}

func isStopKeyword(tok string) bool {
	for _, kw := range stopKeywords {
		if strings.EqualFold(tok, kw) {
			return true
		}
	}

	return false
}

// normalizeIndexColumn quotes a plain column name and drops PostgreSQL
// operator classes (varchar_pattern_ops), which SQLite does not have.
func normalizeIndexColumn(col string) string {
	fields := strings.Fields(col)
	if len(fields) > 1 && strings.HasSuffix(fields[len(fields)-1], "_ops") {
		col = strings.Join(fields[:len(fields)-1], " ")
	}

	if isPlainIdentifier(col) {
		return quoteIdent(col)
	}

	return col
}

func isPlainIdentifier(s string) bool {
	if s == "" {
		return false
	}

	for i, c := range s {
		switch {
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}

	return true
}

// unescapeCopy decodes the backslash sequences of COPY text format.
func unescapeCopy(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}

	var b strings.Builder

	b.Grow(len(s))

	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])

			continue
		}

		i++

		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case '\\':
			b.WriteByte('\\')
		case 'x':
			b.WriteString(`\x`)
		default:
			b.WriteByte(s[i])
		}
	}

	return b.String()
}

func unquoteIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}

	return s
}

func normalizeIdent(s string) string {
	if strings.HasPrefix(s, `"`) {
		return s
	}

	return quoteIdent(s)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// hasPrefixWord reports whether s starts with one of the given uppercase words
// as a whole word.
func hasPrefixWord(s string, words ...string) bool {
	for _, word := range words {
		if !strings.HasPrefix(s, word) {
			continue
		}

		if len(s) == len(word) || s[len(word)] == ' ' || s[len(word)] == '(' {
			return true
		}
	}

	return false
}

// collapseSpace reduces runs of whitespace to one space outside string
// literals, so multi-line statements match the same patterns as single-line
// ones while literal contents keep their spacing.
func collapseSpace(s string) string {
	var b strings.Builder

	b.Grow(len(s))

	inQ, spaced := false, false

	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			inQ = !inQ

			b.WriteByte(c)

			spaced = false
		case !inQ && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if !spaced && b.Len() > 0 {
				b.WriteByte(' ')

				spaced = true
			}
		default:
			b.WriteByte(c)

			spaced = false
		}
	}

	return strings.TrimSpace(b.String())
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}

	return false
}

// indexWordFold finds the first occurrence of needle in s, ignoring case.
// indexWordFold is a case-insensitive strings.Index. Folding a whole string can
// change a rune's byte width (invalid UTF-8, dotted capitals), which would shift
// every index after it, so the fold happens one candidate slice at a time.
func indexWordFold(s, needle string) int {
	for i := 0; i+len(needle) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(needle)], needle) {
			return i
		}
	}

	return -1
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}

	return s
}
