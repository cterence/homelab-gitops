package main

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// collected is one row as the streaming parser handed it over.
type collected struct {
	table string
	cols  []string
	row   []string
}

// parseCollecting parses a dump and records every row the parser emits.
func parseCollecting(t *testing.T, dump string) (*dump, []collected) {
	t.Helper()

	var rows []collected

	parsed, err := parseDump(strings.NewReader(dump), func(tbl *table, cols []string, row []string) error {
		rows = append(rows, collected{table: tbl.name, cols: cols, row: row})

		return nil
	})
	if err != nil {
		t.Fatalf("parseDump: %v", err)
	}

	return parsed, rows
}

// dumpSource writes a dump to a temporary file, which convert needs because it
// reads the file twice.
func dumpSource(t *testing.T, dump string) *os.File {
	t.Helper()

	path := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(path, []byte(dump), 0o600); err != nil {
		t.Fatalf("writing dump: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening dump: %v", err)
	}

	t.Cleanup(func() { _ = file.Close() })

	return file
}

// Tests in this file pin the behaviours that hardening added: the dump shapes
// and PostgreSQL constructs that used to fail or silently produce wrong output.

func TestParsePartitionedTable(t *testing.T) {
	const dump = `CREATE TABLE public.events (
    id bigint NOT NULL,
    created_at timestamp with time zone NOT NULL
)
PARTITION BY RANGE (created_at);

CREATE TABLE public.events_2024 PARTITION OF public.events FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');

CREATE TABLE public.events_2025 PARTITION OF public.events FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');

COPY public.events_2024 (id, created_at) FROM stdin;
1	2024-05-01 00:00:00+00
2	2024-06-01 00:00:00+00
\.
COPY public.events_2025 (id, created_at) FROM stdin;
3	2025-05-01 00:00:00+00
\.
`

	parsed, rows := parseCollecting(t, dump)
	if len(parsed.tables) != 1 {
		t.Fatalf("got %d tables, want only the parent", len(parsed.tables))
	}

	parent := parsed.tables[0]
	if parent.name != "events" {
		t.Errorf("table = %q, want events", parent.name)
	}

	if len(parent.cols) != 2 {
		t.Errorf("parent has %d columns, want 2", len(parent.cols))
	}

	if len(rows) != 3 {
		t.Errorf("streamed %d rows, want 3 from both partitions", len(rows))
	}

	for _, row := range rows {
		if row.table != "events" {
			t.Errorf("row routed to %q, want events", row.table)
		}
	}
}

func TestParseMultipleSchemas(t *testing.T) {
	const dump = `CREATE TABLE public.items (id integer NOT NULL, name text);
CREATE TABLE other.items (id integer NOT NULL, name text);
COPY public.items (id, name) FROM stdin;
1	public-row
\.
COPY other.items (id, name) FROM stdin;
2	other-row
\.
`

	parsed, rows := parseCollecting(t, dump)

	if len(parsed.tables) != 2 {
		t.Fatalf("got %d tables, want 2", len(parsed.tables))
	}

	if parsed.tables[0].name != "items" || parsed.tables[1].name != "other__items" {
		t.Fatalf("names = %q, %q, want items and other__items",
			parsed.tables[0].name, parsed.tables[1].name)
	}

	for _, row := range rows {
		if row.table == "other__items" && row.row[1] != "other-row" {
			t.Errorf("second schema row = %q, want other-row", row.row[1])
		}
	}
}

func TestParseInsertStatements(t *testing.T) {
	const dump = `CREATE TABLE public.users (
    id integer NOT NULL,
    name character varying(50) NOT NULL,
    flag boolean NOT NULL,
    note text
);

INSERT INTO public.users (id, name, flag, note) VALUES (1, 'alice', true, NULL);
INSERT INTO public.users (id, name, flag, note) VALUES (2, 'bob''s', false, E'tab\there'), (3, 'carol', true, 'x');
`

	parsed, collected := parseCollecting(t, dump)

	if len(collected) != 3 {
		t.Fatalf("got %d rows, want 3", len(collected))
	}

	first := collected[0].row
	if first[0] != "1" || first[1] != "alice" || first[2] != "t" || first[3] != `\N` {
		t.Errorf("first row = %q, want 1/alice/t/NULL", first)
	}

	second := collected[1].row
	if second[1] != `bob''s` && second[1] != "bob's" {
		t.Errorf("quoted value = %q, want bob's", second[1])
	}

	if !strings.Contains(second[3], `\t`) {
		t.Errorf("escaped tab = %q, want a COPY-escaped tab", second[3])
	}

	if parsed.tables[0].name != "users" {
		t.Errorf("table = %q, want users", parsed.tables[0].name)
	}
}

func TestParseDollarQuoteTags(t *testing.T) {
	const dump = `CREATE FUNCTION public.touch() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
  UPDATE t SET n = 1;
  RETURN NEW;
END;
$body$;

CREATE TABLE public.t (id integer NOT NULL);
COPY public.t (id) FROM stdin;
1
\.
`

	parsed, rows := parseCollecting(t, dump)

	if len(parsed.tables) != 1 {
		t.Fatalf("got %d tables, want 1", len(parsed.tables))
	}

	if len(rows) != 1 {
		t.Errorf("rows = %d, want 1: the function body must not split statements", len(rows))
	}

	if parsed.skipped["function"] != 1 {
		t.Errorf("skipped functions = %d, want 1", parsed.skipped["function"])
	}
}

func TestParseLongRow(t *testing.T) {
	// Comfortably past the 16 MB bufio.Scanner default that used to cap rows.
	payload := strings.Repeat("a", 17<<20)

	dump := "CREATE TABLE public.big (id integer NOT NULL, payload text);\n" +
		"COPY public.big (id, payload) FROM stdin;\n1\t" + payload + "\n\\.\n"

	_, rows := parseCollecting(t, dump)

	if len(rows) != 1 || len(rows[0].row[1]) != len(payload) {
		t.Fatalf("got %d rows, want 1 with a %d byte payload", len(rows), len(payload))
	}
}

func TestConvertPartitionAndInsert(t *testing.T) {
	const dump = `CREATE TABLE public.events (id bigint NOT NULL, note text) PARTITION BY RANGE (id);
CREATE TABLE public.events_low PARTITION OF public.events FOR VALUES FROM (0) TO (100);
ALTER TABLE ONLY public.events ADD CONSTRAINT events_pkey PRIMARY KEY (id);
COPY public.events_low (id, note) FROM stdin;
1	low
\.
INSERT INTO public.events (id, note) VALUES (200, 'inserted');
`

	db := openTestDB(t)

	result, err := convert(context.Background(), dumpSource(t, dump), db, false, nil)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if result.rows != 2 {
		t.Errorf("rows = %d, want 2 (partition plus insert)", result.rows)
	}

	var note string
	if err := db.QueryRow(`SELECT note FROM events WHERE id = 200`).Scan(&note); err != nil {
		t.Fatalf("querying inserted row: %v", err)
	}

	if note != "inserted" {
		t.Errorf("note = %q, want inserted", note)
	}
}

func TestConvertKeepsChecksAndViews(t *testing.T) {
	const dump = `CREATE TABLE public.t (id integer NOT NULL, n integer, CONSTRAINT t_pkey PRIMARY KEY (id), CONSTRAINT t_n_check CHECK (n > 0));
CREATE VIEW public.positive AS SELECT id FROM public.t WHERE n > 0;
COPY public.t (id, n) FROM stdin;
1	5
\.
`

	db := openTestDB(t)

	result, err := convert(context.Background(), dumpSource(t, dump), db, false, nil)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if result.views != 1 {
		t.Errorf("views = %d, want 1", result.views)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM positive`).Scan(&n); err != nil {
		t.Fatalf("querying view: %v", err)
	}

	if n != 1 {
		t.Errorf("view returned %d rows, want 1", n)
	}

	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 't'`).Scan(&ddl); err != nil {
		t.Fatalf("reading ddl: %v", err)
	}

	if !strings.Contains(ddl, "CHECK") {
		t.Errorf("ddl lost its check constraint: %s", ddl)
	}

	if _, err := db.Exec(`INSERT INTO t (id, n) VALUES (2, -1)`); err == nil {
		t.Error("check constraint is not enforced")
	}
}

func TestConvertDropsBrokenConstraintInsteadOfFailing(t *testing.T) {
	// ~~ is PostgreSQL's LIKE operator; SQLite rejects the check, the table
	// must still be created and reported.
	const dump = `CREATE TABLE public.t (id integer NOT NULL, name text, CONSTRAINT t_check CHECK (name ~~ 'x%'));
COPY public.t (id, name) FROM stdin;
1	hello
\.
`

	db := openTestDB(t)

	result, err := convert(context.Background(), dumpSource(t, dump), db, false, nil)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if result.rows != 1 || result.warnings == 0 {
		t.Errorf("rows = %d warnings = %d, want 1 row and a warning", result.rows, result.warnings)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM t WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("querying: %v", err)
	}

	if name != "hello" {
		t.Errorf("name = %q, want hello", name)
	}
}

func TestConvertStrictRefusesWarnings(t *testing.T) {
	const dump = `CREATE TABLE public.t (id integer NOT NULL, generated integer GENERATED ALWAYS AS (id * 2) STORED);
COPY public.t (id) FROM stdin;
1
\.
`

	db := openTestDB(t)

	if _, err := convert(context.Background(), dumpSource(t, dump), db, true, nil); err == nil {
		t.Fatal("expected strict mode to fail on a warning")
	}
}

func TestNormalizeTimestamp(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "already utc", in: "2024-08-21 17:54:04.853+00", want: "2024-08-21 17:54:04.853+00"},
		{name: "positive offset", in: "2024-01-01 11:00:00+02", want: "2024-01-01 09:00:00+00"},
		{name: "negative offset", in: "2024-01-01 10:00:00-05:30", want: "2024-01-01 15:30:00+00"},
		{name: "offset with minutes", in: "2024-01-01 10:00:00+02:00", want: "2024-01-01 08:00:00+00"},
		{name: "day rollover", in: "2024-01-01 01:00:00+02", want: "2023-12-31 23:00:00+00"},
		{name: "fraction is trimmed", in: "2024-01-01 10:00:00.500000+02", want: "2024-01-01 08:00:00.5+00"},
		{name: "infinity passes through", in: "infinity", want: "infinity"},
		{name: "plain text passes through", in: "not a timestamp", want: "not a timestamp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeTimestamp(tt.in); got != tt.want {
				t.Errorf("normalizeTimestamp(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestConvertNormalizesTimestampsForOrdering(t *testing.T) {
	const dump = `CREATE TABLE public.t (id integer NOT NULL, ts timestamp with time zone NOT NULL);
COPY public.t (id, ts) FROM stdin;
1	2024-01-01 11:00:00+02
2	2024-01-01 10:00:00+00
\.
`

	db := openTestDB(t)

	if _, err := convert(context.Background(), dumpSource(t, dump), db, false, nil); err != nil {
		t.Fatalf("convert: %v", err)
	}

	rows, err := db.Query(`SELECT id FROM t ORDER BY ts`)
	if err != nil {
		t.Fatalf("querying: %v", err)
	}

	defer func() { _ = rows.Close() }()

	var order []int

	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scanning: %v", err)
		}

		order = append(order, id)
	}

	// 11:00+02 is 09:00Z, so it comes before 10:00Z.
	if len(order) != 2 || order[0] != 1 {
		t.Errorf("order = %v, want [1 2] (chronological)", order)
	}
}

func TestConvertMultiSchemaNames(t *testing.T) {
	const dump = `CREATE TABLE public.items (id integer NOT NULL, name text);
CREATE TABLE other.items (id integer NOT NULL, name text);
COPY public.items (id, name) FROM stdin;
1	public-row
\.
COPY other.items (id, name) FROM stdin;
2	other-row
\.
`

	db := openTestDB(t)

	if _, err := convert(context.Background(), dumpSource(t, dump), db, false, nil); err != nil {
		t.Fatalf("convert: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM other__items WHERE id = 2`).Scan(&name); err != nil {
		t.Fatalf("querying other schema table: %v", err)
	}

	if name != "other-row" {
		t.Errorf("name = %q, want other-row", name)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening in-memory sqlite: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db
}

// rowGenerator produces a dump on the fly, so the test never holds the whole
// file in memory either.
type rowGenerator struct {
	rows  int
	value string
	state int
	sent  int
}

func (g *rowGenerator) Read(p []byte) (int, error) {
	var chunk []byte

	switch g.state {
	case 0:
		chunk = []byte("CREATE TABLE public.big (id integer NOT NULL, payload text);\nCOPY public.big (id, payload) FROM stdin;\n")
		g.state = 1
	case 1:
		if g.sent == g.rows {
			chunk = []byte("\\.\n")
			g.state = 2
		} else {
			chunk = []byte(strconv.Itoa(g.sent) + "\t" + g.value + "\n")
			g.sent++
		}
	default:
		return 0, io.EOF
	}

	return copy(p, chunk), nil
}

// Buffering every row would hold the whole dump; streaming keeps the live heap
// flat no matter how many rows pass through.
func TestParseStreamsRows(t *testing.T) {
	const (
		rows  = 200000
		width = 200
	)

	generator := &rowGenerator{rows: rows, value: strings.Repeat("x", width)}

	count := 0

	var peak uint64

	_, err := parseDump(generator, func(_ *table, _ []string, row []string) error {
		count++

		if count%20000 != 0 {
			return nil
		}

		runtime.GC()

		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)

		if stats.HeapAlloc > peak {
			peak = stats.HeapAlloc
		}

		return nil
	})
	if err != nil {
		t.Fatalf("parseDump: %v", err)
	}

	if count != rows {
		t.Fatalf("streamed %d rows, want %d", count, rows)
	}

	// Holding the rows would take rows*width = 40 MB; streaming should stay far
	// below that with a wide margin for the runtime's own allocations.
	if peak > 16<<20 {
		t.Errorf("live heap peaked at %d MB, want under 16 MB", peak>>20)
	}
}

// A stream cut short must fail loudly: committing a partially loaded database
// with exit 0 is the worst outcome a migration tool can produce.
func TestParseRejectsTruncatedDump(t *testing.T) {
	tests := []struct {
		name string
		dump string
	}{
		{
			name: "copy block without terminator",
			dump: "CREATE TABLE public.t (id integer NOT NULL);\nCOPY public.t (id) FROM stdin;\n1\n2\n",
		},
		{
			name: "unterminated statement",
			dump: "CREATE TABLE public.t (id integer NOT NULL);\nCREATE INDEX idx ON public.t (id",
		},
		{
			name: "unterminated dollar quote",
			dump: "CREATE TABLE public.t (id integer NOT NULL);\nCREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1;\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseDump(strings.NewReader(tt.dump), nil); err == nil {
				t.Fatal("expected a truncated dump to be rejected")
			}
		})
	}
}

// EOF straight after a COPY header is the end of the file, not one empty row.
func TestParseDoesNotInventRowAtEOF(t *testing.T) {
	const dump = "CREATE TABLE public.t (id integer NOT NULL);\nCOPY public.t (id) FROM stdin;\n"

	var rows int

	_, err := parseDump(strings.NewReader(dump), func(*table, []string, []string) error {
		rows++

		return nil
	})
	if err == nil {
		t.Fatal("expected a truncated COPY block to be rejected")
	}

	if rows != 0 {
		t.Fatalf("emitted %d rows for an empty COPY block", rows)
	}
}

// A COPY without a column list covers every non-generated column, in order.
func TestParseCopyWithoutColumnList(t *testing.T) {
	const dump = `CREATE TABLE public.t (
    id integer NOT NULL,
    name text,
    doubled integer GENERATED ALWAYS AS (id * 2) STORED
);

COPY public.t FROM stdin;
1	alpha
\.
`

	_, rows := parseCollecting(t, dump)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}

	if got := rows[0].cols; len(got) != 2 || got[0] != "id" || got[1] != "name" {
		t.Fatalf("columns = %v, want [id name]", got)
	}

	if got := rows[0].row; len(got) != 2 || got[0] != "1" || got[1] != "alpha" {
		t.Fatalf("row = %v", got)
	}
}

// A row whose field count disagrees with its column list is corruption, not a
// row to insert misaligned.
func TestParseRejectsRowWithWrongFieldCount(t *testing.T) {
	tests := []struct {
		name string
		dump string
	}{
		{
			name: "copy row",
			dump: "CREATE TABLE public.t (id integer NOT NULL, name text);\nCOPY public.t (id, name) FROM stdin;\n1\n\\.\n",
		},
		{
			name: "copy row without a column list",
			dump: "CREATE TABLE public.t (id integer NOT NULL, name text);\nCOPY public.t FROM stdin;\n1\tx\ty\n\\.\n",
		},
		{
			name: "insert row",
			dump: "CREATE TABLE public.t (id integer NOT NULL, name text);\nINSERT INTO public.t (id, name) VALUES (1);\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDump(strings.NewReader(tt.dump), func(*table, []string, []string) error { return nil })
			if err == nil {
				t.Fatal("expected a row with the wrong number of values to be rejected")
			}
		})
	}
}

func TestParseRejectsTableWithoutName(t *testing.T) {
	if _, err := parseDump(strings.NewReader("CREATE TABLE (0 0);\n"), nil); err == nil {
		t.Fatal("expected a CREATE TABLE without a name to be rejected")
	}
}
