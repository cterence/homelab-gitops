package main

import (
	"context"
	"database/sql"
	"testing"
)

const endToEndDump = `--
-- PostgreSQL database dump
--
\restrict token
SET statement_timeout = 0;

CREATE TABLE public.version (
    id integer NOT NULL,
    version character varying(64) DEFAULT ''::character varying NOT NULL
);

CREATE TABLE public.access (
    id bigint NOT NULL,
    user_id bigint,
    mode integer DEFAULT 0 NOT NULL,
    is_restricted boolean DEFAULT false NOT NULL,
    payload bytea
);

ALTER TABLE ONLY public.version
    ADD CONSTRAINT version_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.access
    ADD CONSTRAINT access_pkey PRIMARY KEY (id);

CREATE UNIQUE INDEX "UQE_access_s" ON public.access USING btree (user_id);

COPY public.version (id, version) FROM stdin;
1	343
\.

COPY public.access (id, user_id, mode, is_restricted, payload) FROM stdin;
1	7	4	t	\\x0102ff
2	\N	0	f	\N
\.

\unrestrict token
`

func TestConvertEndToEnd(t *testing.T) {
	ctx := context.Background()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening in-memory sqlite: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	result, err := convert(ctx, dumpSource(t, endToEndDump), db, false)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	counts := result.tables

	if len(counts) != 2 {
		t.Fatalf("got %d tables, want 2: %+v", len(counts), counts)
	}

	for _, c := range counts {
		want := 1
		if c.table == "access" {
			want = 2
		}

		if c.rows != want {
			t.Errorf("table %s has %d rows, want %d", c.table, c.rows, want)
		}
	}

	t.Run("data lands with sqlite types", func(t *testing.T) {
		var (
			id           int64
			userID       sql.NullInt64
			mode         int64
			isRestricted int64
			payload      []byte
			modeType     string
			boolType     string
		)

		err := db.QueryRowContext(ctx,
			`SELECT id, user_id, mode, is_restricted, payload, typeof(mode), typeof(is_restricted)
			 FROM access WHERE id = 1`).
			Scan(&id, &userID, &mode, &isRestricted, &payload, &modeType, &boolType)
		if err != nil {
			t.Fatalf("querying access: %v", err)
		}

		if id != 1 || userID.Int64 != 7 || mode != 4 || isRestricted != 1 {
			t.Errorf("row = %d %d %d %d", id, userID.Int64, mode, isRestricted)
		}

		if modeType != "integer" || boolType != "integer" {
			t.Errorf("types = %s/%s, want integer/integer", modeType, boolType)
		}

		if string(payload) != "\x01\x02\xff" {
			t.Errorf("payload = %v, want the decoded bytea", payload)
		}
	})

	t.Run("nulls survive", func(t *testing.T) {
		var (
			userID  sql.NullInt64
			payload []byte
		)

		err := db.QueryRowContext(ctx, `SELECT user_id, payload FROM access WHERE id = 2`).Scan(&userID, &payload)
		if err != nil {
			t.Fatalf("querying: %v", err)
		}

		if userID.Valid {
			t.Errorf("user_id = %d, want NULL", userID.Int64)
		}

		if payload != nil {
			t.Errorf("payload = %v, want NULL", payload)
		}
	})

	t.Run("primary keys and unique indexes are preserved", func(t *testing.T) {
		var pk int

		if err := db.QueryRowContext(ctx,
			`SELECT pk FROM pragma_table_info('access') WHERE name = 'id'`).Scan(&pk); err != nil {
			t.Fatalf("reading table_info: %v", err)
		}

		if pk != 1 {
			t.Errorf("access.id pk = %d, want 1", pk)
		}

		var name string

		err := db.QueryRowContext(ctx,
			`SELECT name FROM pragma_index_list('access') WHERE "unique" = 1 AND origin = 'c'`).Scan(&name)
		if err != nil {
			t.Fatalf("reading index_list: %v", err)
		}

		if name != "UQE_access_s" {
			t.Errorf("unique index = %q", name)
		}
	})
}

func TestConvertRejectsDumpWithoutTables(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening in-memory sqlite: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	// A dump that yields nothing is far more likely a mistake (wrong format,
	// empty file) than an empty database.
	if _, err := convert(context.Background(), dumpSource(t, "-- nothing here\n"), db, false); err == nil {
		t.Fatal("expected an error for a dump with no tables")
	}
}

// A generated column has no stored value, so pg_dump leaves it out of COPY.
// The remaining values must still land in their own columns.
func TestConvertGeneratedColumn(t *testing.T) {
	const dump = `CREATE TABLE public.custom (
    id integer NOT NULL,
    value_monetary numeric(65,2),
    value_monetary_amount numeric(65,2) GENERATED ALWAYS AS (value_monetary * 2) STORED,
    note text
);

ALTER TABLE ONLY public.custom
    ADD CONSTRAINT custom_pkey PRIMARY KEY (id);

COPY public.custom (id, value_monetary, note) FROM stdin;
1	12.34	first
2	\N	second
\.
`

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening in-memory sqlite: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	if _, err := convert(context.Background(), dumpSource(t, dump), db, false); err != nil {
		t.Fatalf("convert: %v", err)
	}

	var (
		monetary sql.NullFloat64
		amount   sql.NullFloat64
		note     string
	)

	err = db.QueryRow(`SELECT value_monetary, value_monetary_amount, note
	                   FROM custom WHERE id = 1`).Scan(&monetary, &amount, &note)
	if err != nil {
		t.Fatalf("querying custom: %v", err)
	}

	if monetary.Float64 != 12.34 || note != "first" {
		t.Errorf("row = %v %q, want 12.34 first", monetary.Float64, note)
	}

	if amount.Valid {
		t.Errorf("generated column = %v, want NULL", amount.Float64)
	}
}
