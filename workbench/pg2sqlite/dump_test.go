package main

import (
	"strings"
	"testing"
)

func TestSplitTopLevel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "plain columns",
			in:   "id integer NOT NULL, name text",
			want: []string{"id integer NOT NULL", "name text"},
		},
		{
			name: "commas inside parens",
			in:   "amount numeric(10,2), note text",
			want: []string{"amount numeric(10,2)", "note text"},
		},
		{
			name: "commas inside quotes",
			in:   "kind text DEFAULT 'a,b', flag boolean",
			want: []string{"kind text DEFAULT 'a,b'", "flag boolean"},
		},
		{
			name: "escaped quote inside default",
			in:   "kind text DEFAULT 'it''s, ok', flag boolean",
			want: []string{"kind text DEFAULT 'it''s, ok'", "flag boolean"},
		},
		{
			name: "newlines between items",
			in:   "id integer,\n    name text\n",
			want: []string{"id integer", "name text"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitTopLevel(tt.in, ',')
			if len(got) != len(tt.want) {
				t.Fatalf("got %d parts %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("part %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// parseTableStmt parses a dump made of a single statement and returns its
// first table.
func parseTableStmt(stmt string) (*table, error) {
	d, err := parseDump(strings.NewReader(stmt+"\n"), nil)
	if err != nil {
		return nil, err
	}

	return d.tables[0], nil
}

func TestParseCreateTable(t *testing.T) {
	tests := []struct {
		name  string
		stmt  string
		want  *table
		fails bool
	}{
		{
			name: "wide column list",
			stmt: `CREATE TABLE public.access (
    id bigint NOT NULL,
    user_id bigint,
    mode integer,
    created_unix bigint DEFAULT 0 NOT NULL,
    is_restricted boolean DEFAULT false NOT NULL,
    name character varying(255) DEFAULT ''::character varying NOT NULL
);`,
			want: &table{
				name: "access",
				cols: []column{
					{name: "id", pgType: "bigint", sqlType: "INTEGER", notNull: true},
					{name: "user_id", pgType: "bigint", sqlType: "INTEGER"},
					{name: "mode", pgType: "integer", sqlType: "INTEGER"},
					{name: "created_unix", pgType: "bigint", sqlType: "INTEGER", notNull: true, def: "0"},
					{name: "is_restricted", pgType: "boolean", sqlType: "INTEGER", notNull: true, def: "0"},
					{name: "name", pgType: "character varying(255)", sqlType: "TEXT", notNull: true, def: "''"},
				},
			},
		},
		{
			name: "mixed column types",
			stmt: `CREATE TABLE public.documents_document (
    id integer NOT NULL,
    checksum character varying(32) NOT NULL,
    content text,
    metadata jsonb,
    created timestamp with time zone NOT NULL,
    remote_id uuid,
    score double precision,
    amount numeric(10,2)
);`,
			want: &table{
				name: "documents_document",
				cols: []column{
					{name: "id", pgType: "integer", sqlType: "INTEGER", notNull: true},
					{name: "checksum", pgType: "character varying(32)", sqlType: "TEXT", notNull: true},
					{name: "content", pgType: "text", sqlType: "TEXT"},
					{name: "metadata", pgType: "jsonb", sqlType: "TEXT"},
					{name: "created", pgType: "timestamp with time zone", sqlType: "TEXT", notNull: true},
					{name: "remote_id", pgType: "uuid", sqlType: "TEXT"},
					{name: "score", pgType: "double precision", sqlType: "REAL"},
					{name: "amount", pgType: "numeric(10,2)", sqlType: "NUMERIC"},
				},
			},
		},
		{
			name: "inline primary key and table constraint",
			stmt: `CREATE TABLE public.joins (
    a integer NOT NULL,
    b integer NOT NULL,
    note text,
    CONSTRAINT joins_pkey PRIMARY KEY (a, b)
);`,
			want: &table{
				name: "joins",
				cols: []column{
					{name: "a", pgType: "integer", sqlType: "INTEGER", notNull: true},
					{name: "b", pgType: "integer", sqlType: "INTEGER", notNull: true},
					{name: "note", pgType: "text", sqlType: "TEXT"},
				},
				pk: []string{"a", "b"},
			},
		},
		{
			name: "quoted identifiers",
			stmt: `CREATE TABLE public."Order" (
    "user" integer NOT NULL,
    "index" text
);`,
			want: &table{
				name: "Order",
				cols: []column{
					{name: "user", pgType: "integer", sqlType: "INTEGER", notNull: true},
					{name: "index", pgType: "text", sqlType: "TEXT"},
				},
			},
		},
		{
			name:  "not a create table",
			stmt:  "CREATE SEQUENCE public.access_id_seq START WITH 1;",
			fails: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTableStmt(tt.stmt)
			if tt.fails {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("parseCreateTable: %v", err)
			}

			if got.name != tt.want.name {
				t.Errorf("table = %q, want %q", got.name, tt.want.name)
			}

			if len(got.cols) != len(tt.want.cols) {
				t.Fatalf("got %d columns, want %d: %+v", len(got.cols), len(tt.want.cols), got.cols)
			}

			for i, wantCol := range tt.want.cols {
				if got.cols[i] != wantCol {
					t.Errorf("column %d = %+v, want %+v", i, got.cols[i], wantCol)
				}
			}

			if strings.Join(got.pk, ",") != strings.Join(tt.want.pk, ",") {
				t.Errorf("pk = %v, want %v", got.pk, tt.want.pk)
			}
		})
	}
}

func TestParseAddConstraint(t *testing.T) {
	tests := []struct {
		name      string
		stmt      string
		wantTable string
		wantKind  string
		wantCols  []string
		wantOK    bool
	}{
		{
			name:      "primary key across lines",
			stmt:      "ALTER TABLE ONLY public.access\n    ADD CONSTRAINT access_pkey PRIMARY KEY (id);",
			wantTable: "public.access",
			wantKind:  "primary",
			wantCols:  []string{"id"},
			wantOK:    true,
		},
		{
			name:      "composite primary key",
			stmt:      "ALTER TABLE ONLY public.action_run_attempt_job_id_index ADD CONSTRAINT x_pkey PRIMARY KEY (group_id);",
			wantTable: "public.action_run_attempt_job_id_index",
			wantKind:  "primary",
			wantCols:  []string{"group_id"},
			wantOK:    true,
		},
		{
			name:      "unique constraint",
			stmt:      "ALTER TABLE ONLY public.foo ADD CONSTRAINT foo_key UNIQUE (a, b);",
			wantTable: "public.foo",
			wantKind:  "unique",
			wantCols:  []string{"a", "b"},
			wantOK:    true,
		},
		{
			name:     "foreign key is ignored",
			stmt:     "ALTER TABLE ONLY public.foo ADD CONSTRAINT foo_fk FOREIGN KEY (bar_id) REFERENCES public.bar(id);",
			wantOK:   false,
			wantKind: "",
		},
		{
			name:   "set default is ignored",
			stmt:   "ALTER TABLE ONLY public.access ALTER COLUMN id SET DEFAULT nextval('public.access_id_seq'::regclass);",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTable, gotKind, gotCols, ok := parseAddConstraint(tt.stmt)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}

			if !ok {
				return
			}

			if gotTable != tt.wantTable {
				t.Errorf("table = %q, want %q", gotTable, tt.wantTable)
			}

			if gotKind != tt.wantKind {
				t.Errorf("kind = %q, want %q", gotKind, tt.wantKind)
			}

			if strings.Join(gotCols, ",") != strings.Join(tt.wantCols, ",") {
				t.Errorf("cols = %v, want %v", gotCols, tt.wantCols)
			}
		})
	}
}

func TestRewriteIndex(t *testing.T) {
	tests := []struct {
		name      string
		stmt      string
		wantTable string
		wantSQL   string
		wantOK    bool
	}{
		{
			name:      "btree index loses USING and schema",
			stmt:      `CREATE INDEX "IDX_access_token_uid" ON public.access_token USING btree (uid);`,
			wantTable: "public.access_token",
			wantSQL:   `CREATE INDEX "IDX_access_token_uid" ON "access_token" ("uid");`,
			wantOK:    true,
		},
		{
			name:      "unique index",
			stmt:      `CREATE UNIQUE INDEX "UQE_access_s" ON public.access USING btree (user_id, repo_id);`,
			wantTable: "public.access",
			wantSQL:   `CREATE UNIQUE INDEX "UQE_access_s" ON "access" ("user_id", "repo_id");`,
			wantOK:    true,
		},
		{
			name:      "partial index keeps its WHERE",
			stmt:      `CREATE INDEX idx_open ON public.issues USING btree (repo_id) WHERE (is_closed = false);`,
			wantTable: "public.issues",
			wantSQL:   `CREATE INDEX "idx_open" ON "issues" ("repo_id") WHERE (is_closed = false);`,
			wantOK:    true,
		},
		{
			name:      "postgres operator class is dropped",
			stmt:      `CREATE INDEX "auth_group_name_a6ea08ec_like" ON public.auth_group USING btree (name varchar_pattern_ops);`,
			wantTable: "public.auth_group",
			wantSQL:   `CREATE INDEX "auth_group_name_a6ea08ec_like" ON "auth_group" ("name");`,
			wantOK:    true,
		},
		{
			name:      "any array becomes in",
			stmt:      `CREATE UNIQUE INDEX "token_kind_uniq" ON public.tokens USING btree (user_id, kind) WHERE ((kind)::text = ANY (ARRAY['totp'::character varying, 'recovery'::character varying]));`,
			wantTable: "public.tokens",
			wantSQL:   `CREATE UNIQUE INDEX "token_kind_uniq" ON "tokens" ("user_id", "kind") WHERE ((kind) IN ('totp', 'recovery'));`,
			wantOK:    true,
		},
		{
			name:      "json path operator becomes json_extract",
			stmt:      `CREATE INDEX idx_sessions_client_subject ON public.sessions USING btree (client_id, ((request_data #>> '{session,subject}'::text[])), kind, active);`,
			wantTable: "public.sessions",
			wantSQL:   `CREATE INDEX "idx_sessions_client_subject" ON "sessions" ("client_id", ((json_extract(request_data, '$.session.subject'))), "kind", "active");`,
			wantOK:    true,
		},
		{
			name:      "at time zone is dropped",
			stmt:      `CREATE INDEX asset_localDateTime_idx ON public.asset USING btree (((("localDateTime" AT TIME ZONE 'UTC'::text))));`,
			wantTable: "public.asset",
			wantSQL:   `CREATE INDEX "asset_localDateTime_idx" ON "asset" (((("localDateTime"))));`,
			wantOK:    true,
		},
		{
			name:    "non-btree index is skipped",
			stmt:    `CREATE INDEX idx_gin ON public.docs USING gin (metadata);`,
			wantOK:  false,
			wantSQL: "",
		},
		{
			name:   "not an index",
			stmt:   "CREATE TABLE public.t (id integer);",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTable, gotSQL, ok := rewriteIndex(tt.stmt, func(ref string) string {
				schema, name := splitTableRef(ref)

				return sqliteTableName(schema, name)
			})
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (sql %q)", ok, tt.wantOK, gotSQL)
			}

			if !ok {
				return
			}

			if gotTable != tt.wantTable {
				t.Errorf("table = %q, want %q", gotTable, tt.wantTable)
			}

			if gotSQL != tt.wantSQL {
				t.Errorf("sql = %q, want %q", gotSQL, tt.wantSQL)
			}
		})
	}
}

func TestParseDump(t *testing.T) {
	dump := `--
-- PostgreSQL database dump
--

\restrict 2i2jB8dfKb28u8TDqE1y7acbuYmJIFnkd028tf4rU0cvW2lCMfhG0Ilg6her8sP

SET statement_timeout = 0;
SELECT pg_catalog.set_config('search_path', '', false);

CREATE TABLE public.foo (
    id integer NOT NULL,
    name character varying(255) DEFAULT ''::character varying NOT NULL,
    active boolean DEFAULT false NOT NULL
);

ALTER TABLE public.foo OWNER TO postgres;

CREATE SEQUENCE public.foo_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER TABLE ONLY public.foo
    ADD CONSTRAINT foo_pkey PRIMARY KEY (id);

CREATE INDEX "IDX_foo_name" ON public.foo USING btree (name);

COPY public.foo (id, name, active) FROM stdin;
1	fred	t
2	\N	f
\.

CREATE TABLE public.bar (
    id integer NOT NULL,
    foo_id integer
);

ALTER TABLE ONLY public.bar
    ADD CONSTRAINT bar_pkey PRIMARY KEY (id);

COPY public.bar (id, foo_id) FROM stdin;
\.

\unrestrict 2i2jB8dfKb28u8TDqE1y7acbuYmJIFnkd028tf4rU0cvW2lCMfhG0Ilg6her8sP

--
-- PostgreSQL database dump complete
--
`

	parsed, rows := parseCollecting(t, dump)
	if len(parsed.tables) != 2 {
		t.Fatalf("got %d tables, want 2", len(parsed.tables))
	}

	foo := parsed.tables[0]
	if foo.name != "foo" {
		t.Fatalf("first table = %q, want foo", foo.name)
	}

	if len(foo.cols) != 3 {
		t.Errorf("foo has %d columns, want 3", len(foo.cols))
	}

	if strings.Join(foo.pk, ",") != "id" {
		t.Errorf("foo pk = %v, want [id]", foo.pk)
	}

	if len(foo.indexes) != 1 {
		t.Fatalf("foo has %d indexes, want 1", len(foo.indexes))
	}

	if foo.indexes[0] != `CREATE INDEX "IDX_foo_name" ON "foo" ("name");` {
		t.Errorf("index = %q", foo.indexes[0])
	}

	var fooRows []collected

	for _, row := range rows {
		if row.table == "foo" {
			fooRows = append(fooRows, row)
		}
	}

	if len(fooRows) != 2 {
		t.Fatalf("foo has %d rows, want 2", len(fooRows))
	}

	if got := fooRows[1].row[1]; got != `\N` {
		t.Errorf("null field = %q, want the raw \\N marker", got)
	}

	if bar := parsed.tables[1]; bar.name != "bar" || len(rows) != 2 {
		t.Errorf("bar = %q, and %d rows were streamed, want an empty bar", bar.name, len(rows))
	}

	if parsed.skipped["sequence"] != 1 {
		t.Errorf("skipped sequences = %d, want 1", parsed.skipped["sequence"])
	}
}

func TestParseDumpRejectsUnknownCopyTarget(t *testing.T) {
	dump := "COPY public.missing (id) FROM stdin;\n1\n\\.\n"

	if _, err := parseDump(strings.NewReader(dump), nil); err == nil {
		t.Fatal("expected an error for a COPY into a table that was never created")
	}
}

func TestStripCasts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain cast",
			in:   `on "auth_group" ("name")`,
			want: `on "auth_group" ("name")`,
		},
		{
			name: "multi word type",
			in:   `WHERE ((type) = ANY (ARRAY[('totp'::character varying)]))`,
			want: `WHERE ((type) = ANY (ARRAY[('totp')]))`,
		},
		{
			name: "array suffix",
			in:   `(request_data #>> '{session,subject}')::text[]`,
			want: `(request_data #>> '{session,subject}')`,
		},
		{
			name: "timestamp with time zone",
			in:   `(("localDateTime" AT TIME ZONE 'UTC'::text))::timestamp with time zone`,
			want: `(("localDateTime" AT TIME ZONE 'UTC'))`,
		},
		{
			name: "enum cast inside a predicate",
			in:   `WHERE (role = 'owner'::public.album_user_role_enum)`,
			want: `WHERE (role = 'owner')`,
		},
		{
			name: "json operator survives",
			in:   `data ->> 'clientName'::text`,
			want: `data ->> 'clientName'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripCasts(tt.in); got != tt.want {
				t.Errorf("stripCasts(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
