package main

import (
	"bytes"
	"testing"
)

func TestMapType(t *testing.T) {
	tests := []struct {
		name   string
		pgType string
		want   string
	}{
		{name: "integer", pgType: "integer", want: "INTEGER"},
		{name: "bigint", pgType: "bigint", want: "INTEGER"},
		{name: "smallint", pgType: "smallint", want: "INTEGER"},
		{name: "serial", pgType: "serial", want: "INTEGER"},
		{name: "bigserial", pgType: "bigserial", want: "INTEGER"},
		{name: "boolean", pgType: "boolean", want: "INTEGER"},
		{name: "varchar with length", pgType: "character varying(255)", want: "TEXT"},
		{name: "text", pgType: "text", want: "TEXT"},
		{name: "citext", pgType: "citext", want: "TEXT"},
		{name: "jsonb", pgType: "jsonb", want: "TEXT"},
		{name: "json", pgType: "json", want: "TEXT"},
		{name: "uuid", pgType: "uuid", want: "TEXT"},
		{name: "timestamptz", pgType: "timestamp with time zone", want: "TEXT"},
		{name: "timestamp", pgType: "timestamp without time zone", want: "TEXT"},
		{name: "date", pgType: "date", want: "TEXT"},
		{name: "interval", pgType: "interval", want: "TEXT"},
		{name: "numeric with precision", pgType: "numeric(10,2)", want: "NUMERIC"},
		{name: "money", pgType: "money", want: "NUMERIC"},
		{name: "double precision", pgType: "double precision", want: "REAL"},
		{name: "real", pgType: "real", want: "REAL"},
		{name: "bytea", pgType: "bytea", want: "BLOB"},
		{name: "integer array", pgType: "integer[]", want: "TEXT"},
		{name: "schema qualified enum", pgType: "public.doc_state", want: "TEXT"},
		{name: "unknown type", pgType: "tsvector", want: "TEXT"},
		{name: "mixed case", pgType: "INTEGER", want: "INTEGER"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mapType(tt.pgType); got != tt.want {
				t.Errorf("mapType(%q) = %q, want %q", tt.pgType, got, tt.want)
			}
		})
	}
}

func TestMapDefault(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		want   string
		wantOK bool
	}{
		{name: "nextval is dropped", expr: "nextval('public.access_id_seq'::regclass)", wantOK: false},
		{name: "now becomes CURRENT_TIMESTAMP", expr: "now()", want: "CURRENT_TIMESTAMP", wantOK: true},
		{name: "true becomes 1", expr: "true", want: "1", wantOK: true},
		{name: "false becomes 0", expr: "false", want: "0", wantOK: true},
		{name: "casted empty string", expr: "''::character varying", want: "''", wantOK: true},
		{name: "casted string", expr: "'draft'::public.doc_state", want: "'draft'", wantOK: true},
		{name: "plain integer", expr: "0", want: "0", wantOK: true},
		{name: "gen_random_uuid is dropped", expr: "gen_random_uuid()", wantOK: false},
		{name: "now in a time zone", expr: "(now() AT TIME ZONE 'utc'::text)", want: "CURRENT_TIMESTAMP", wantOK: true},
		{name: "timezone helper", expr: "timezone('UTC'::text, now())", want: "CURRENT_TIMESTAMP", wantOK: true},
		{name: "custom uuid function is dropped", expr: "public.custom_uuid_v7()", wantOK: false},
		{name: "parenthesised integer", expr: "((0))", want: "0", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mapDefault(tt.expr)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tt.wantOK, got)
			}

			if ok && got != tt.want {
				t.Errorf("mapDefault(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}

func TestCreateTableSQL(t *testing.T) {
	tests := []struct {
		name  string
		table *table
		want  string
	}{
		{
			name: "single integer primary key is inlined",
			table: &table{
				name: "foo",
				cols: []column{
					{name: "id", pgType: "bigint", sqlType: "INTEGER", notNull: true},
					{name: "name", pgType: "text", sqlType: "TEXT", def: "''", notNull: true},
					{name: "active", pgType: "boolean", sqlType: "INTEGER", def: "0", notNull: true},
				},
				pk: []string{"id"},
			},
			want: `CREATE TABLE "foo" (
  "id" INTEGER PRIMARY KEY,
  "name" TEXT NOT NULL DEFAULT '',
  "active" INTEGER NOT NULL DEFAULT 0
)`,
		},
		{
			name: "composite primary key becomes a table constraint",
			table: &table{
				name: "joins",
				cols: []column{
					{name: "a", pgType: "integer", sqlType: "INTEGER", notNull: true},
					{name: "b", pgType: "integer", sqlType: "INTEGER", notNull: true},
				},
				pk: []string{"a", "b"},
			},
			want: `CREATE TABLE "joins" (
  "a" INTEGER NOT NULL,
  "b" INTEGER NOT NULL,
  PRIMARY KEY ("a", "b")
)`,
		},
		{
			name: "single non integer primary key stays a constraint",
			table: &table{
				name: "tokens",
				cols: []column{
					{name: "id", pgType: "uuid", sqlType: "TEXT", notNull: true},
				},
				pk:     []string{"id"},
				unique: [][]string{{"id"}},
			},
			want: `CREATE TABLE "tokens" (
  "id" TEXT NOT NULL,
  PRIMARY KEY ("id"),
  UNIQUE ("id")
)`,
		},
		{
			name: "reserved words are quoted",
			table: &table{
				name: "Order",
				cols: []column{
					{name: "index", pgType: "text", sqlType: "TEXT"},
					{name: `we"ird`, pgType: "text", sqlType: "TEXT"},
				},
			},
			want: `CREATE TABLE "Order" (
  "index" TEXT,
  "we""ird" TEXT
)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := createTableSQL(tt.table, true, true); got != tt.want {
				t.Errorf("createTableSQL:\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestRowValues(t *testing.T) {
	cols := &table{
		name: "foo",
		cols: []column{
			{name: "id", pgType: "bigint", sqlType: "INTEGER"},
			{name: "name", pgType: "text", sqlType: "TEXT"},
			{name: "active", pgType: "boolean", sqlType: "INTEGER"},
			{name: "blob", pgType: "bytea", sqlType: "BLOB"},
		},
	}

	tests := []struct {
		name    string
		fields  []string
		want    []any
		wantErr bool
	}{
		{
			name:   "plain row",
			fields: []string{"1", "fred", "t", "\\x0102"},
			want:   []any{"1", "fred", int64(1), []byte{0x01, 0x02}},
		},
		{
			name:   "null marker and false",
			fields: []string{"2", `\N`, "f", `\N`},
			want:   []any{"2", nil, int64(0), nil},
		},
		{
			name:   "escapes are decoded",
			fields: []string{"3", `a\tb\nc\\d`, "t", `\N`},
			want:   []any{"3", "a\tb\nc\\d", int64(1), nil},
		},
		{
			name:    "wrong field count",
			fields:  []string{"1", "fred"},
			wantErr: true,
		},
		{
			name:    "invalid bytea hex",
			fields:  []string{"1", "fred", "t", `\xzz`},
			wantErr: true,
		},
		{
			name:   "bytea escape format",
			fields: []string{"4", "fred", "t", `\\001\\002\\377`},
			want:   []any{"4", "fred", int64(1), []byte{0x01, 0x02, 0xff}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rowValues(cols, cols.insertColumns(nil), tt.fields, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("rowValues: %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d values, want %d", len(got), len(tt.want))
			}

			for i := range got {
				switch want := tt.want[i].(type) {
				case []byte:
					gotBytes, ok := got[i].([]byte)
					if !ok || !bytes.Equal(gotBytes, want) {
						t.Errorf("value %d = %v, want %v", i, got[i], want)
					}
				default:
					if got[i] != want {
						t.Errorf("value %d = %#v, want %#v", i, got[i], want)
					}
				}
			}
		})
	}
}

func TestUnescapeCopy(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "tab", in: `a\tb`, want: "a\tb"},
		{name: "newline", in: `a\nb`, want: "a\nb"},
		{name: "carriage return", in: `a\rb`, want: "a\rb"},
		{name: "backslash", in: `a\\b`, want: `a\b`},
		{name: "bytea prefix survives", in: `\\x0102`, want: `\x0102`},
		{name: "literal", in: "plain", want: "plain"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unescapeCopy(tt.in); got != tt.want {
				t.Errorf("unescapeCopy(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "plain", want: `"plain"`},
		{name: "embedded quote", in: `we"ird`, want: `"we""ird"`},
		{name: "space", in: "a b", want: `"a b"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quoteIdent(tt.in); got != tt.want {
				t.Errorf("quoteIdent(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}
