package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const refDump = `
CREATE TABLE public.users (
    id text NOT NULL,
    created_at timestamp with time zone,
    is_admin boolean DEFAULT false NOT NULL,
    username character varying(64) NOT NULL
);

CREATE TABLE public.hosts (
    host_id text NOT NULL,
    host_last_health_check timestamp with time zone NOT NULL
);

CREATE TABLE public.dropped (
    id integer NOT NULL
);

CREATE INDEX users_created_at ON public.users USING btree (created_at);

COPY public.users (id, created_at, is_admin, username) FROM stdin;
u1	2026-10-26 20:38:28.056139+00	t	Terence
\.

COPY public.hosts (host_id, host_last_health_check) FROM stdin;
h1	2026-10-26 20:38:28.056139+00
\.

COPY public.dropped (id) FROM stdin;
1
\.
`

const refSchemaDDL = `
CREATE TABLE "users" (
    id TEXT NOT NULL PRIMARY KEY,
    created_at DATETIME,
    is_admin BOOLEAN DEFAULT FALSE NOT NULL,
    username TEXT COLLATE NOCASE NOT NULL UNIQUE
);

CREATE TABLE hosts (
    host_id TEXT NOT NULL PRIMARY KEY,
    host_last_health_check INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE INDEX users_created_at ON users (created_at);
`

func TestRowValuesFollowsReferenceTypes(t *testing.T) {
	tests := []struct {
		name   string
		column column
		field  string
		want   any
	}{
		{
			name:   "datetime column gets the driver's own layout",
			column: column{name: "created_at", pgType: "timestamp with time zone", sqlType: "DATETIME"},
			field:  "2026-10-26 20:38:28.056139+00",
			want:   "2026-10-26 20:38:28.056139+00:00",
		},
		{
			name:   "integer column holds epoch milliseconds",
			column: column{name: "host_last_health_check", pgType: "timestamp with time zone", sqlType: "integer"},
			field:  "2026-10-26 20:38:28.056139+00",
			want:   time.Date(2026, 10, 26, 20, 38, 28, 56139000, time.UTC).UnixMilli(),
		},
		{
			name:   "integer column holds interval milliseconds",
			column: column{name: "actor_idle_timeout", pgType: "interval", sqlType: "integer"},
			field:  "00:05:00",
			want:   int64(300000),
		},
		{
			name:   "interval over days keeps its days",
			column: column{name: "retention", pgType: "interval", sqlType: "integer"},
			field:  "1 day 02:03:04.5",
			want:   int64(93784500),
		},
		{
			name:   "boolean column gets 1",
			column: column{name: "is_admin", pgType: "boolean", sqlType: "BOOLEAN"},
			field:  "t",
			want:   int64(1),
		},
		{
			name:   "integer column flagged boolean gets 0",
			column: column{name: "disabled", pgType: "boolean", sqlType: "INTEGER"},
			field:  "f",
			want:   int64(0),
		},
		{
			name:   "text column keeps the postgres text form",
			column: column{name: "door_time", pgType: "timestamp with time zone", sqlType: "TEXT"},
			field:  "2026-10-26 20:38:28.056139+02",
			want:   "2026-10-26 18:38:28.056139+00",
		},
		{
			name:   "timestamp without an offset is read as utc",
			column: column{name: "created_at", pgType: "timestamp without time zone", sqlType: "DATETIME"},
			field:  "2026-10-26 20:38:28",
			want:   "2026-10-26 20:38:28+00:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := &table{name: "t", cols: []column{tt.column}}

			got, err := rowValues(tbl, []*column{&tbl.cols[0]}, nil, []string{tt.field}, nil)
			if err != nil {
				t.Fatalf("rowValues: %v", err)
			}

			if got[0] != tt.want {
				t.Fatalf("got %#v, want %#v", got[0], tt.want)
			}
		})
	}
}

func TestConvertFollowsReferenceSchema(t *testing.T) {
	ctx := context.Background()

	ref, err := loadRefSchema(writeRefSchema(t))
	if err != nil {
		t.Fatalf("loading reference schema: %v", err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening in-memory sqlite: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	result, err := convertTo(ctx, dumpSource(t, refDump), db, false, nil, ref)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if len(result.tables) != 2 {
		t.Fatalf("got %d tables, want 2: %+v", len(result.tables), result.tables)
	}

	if result.warnings != 1 {
		t.Fatalf("got %d warnings, want 1 for the table the reference lacks", result.warnings)
	}

	var (
		createdAt time.Time
		isAdmin   int
		username  string
	)

	err = db.QueryRow(`SELECT created_at, is_admin, username FROM users`).Scan(&createdAt, &isAdmin, &username)
	if err != nil {
		t.Fatalf("reading users: %v", err)
	}

	if want := time.Date(2026, 10, 26, 20, 38, 28, 56139000, time.UTC); !createdAt.Equal(want) {
		t.Fatalf("created_at is %s, want %s", createdAt, want)
	}

	if isAdmin != 1 || username != "Terence" {
		t.Fatalf("got is_admin=%d username=%q", isAdmin, username)
	}

	var (
		kind string
		ms   int64
	)

	err = db.QueryRow(`SELECT typeof(host_last_health_check), host_last_health_check FROM hosts`).Scan(&kind, &ms)
	if err != nil {
		t.Fatalf("reading hosts: %v", err)
	}

	if kind != "integer" {
		t.Fatalf("host timestamp stored as %s, want integer", kind)
	}

	if want := time.Date(2026, 10, 26, 20, 38, 28, 56139000, time.UTC).UnixMilli(); ms != want {
		t.Fatalf("got %d epoch milliseconds, want %d", ms, want)
	}

	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'users'`).Scan(&ddl); err != nil {
		t.Fatalf("reading users ddl: %v", err)
	}

	if !strings.Contains(ddl, "COLLATE NOCASE") {
		t.Fatalf("the reference collation was lost: %s", ddl)
	}

	var index string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'users_created_at'`).Scan(&index); err != nil {
		t.Fatalf("the reference index was not created: %v", err)
	}
}

// writeRefSchema writes a database shaped like one an app creates for itself.
func writeRefSchema(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ref.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening reference database: %v", err)
	}

	if _, err := db.Exec(refSchemaDDL); err != nil {
		t.Fatalf("creating reference schema: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("closing reference database: %v", err)
	}

	return path
}
