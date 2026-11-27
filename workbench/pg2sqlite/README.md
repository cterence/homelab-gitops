# pg2sqlite

Converts a plain-format `pg_dump` artifact into a SQLite database, so an app
can be moved from PostgreSQL to SQLite. The schema is read first, the rows are
then streamed into the tables in a single transaction, and the indexes and
views are built last.

## Usage

```bash
# Next to the source database
pg_dump -U postgres -d mydb --no-owner --no-privileges > mydb.sql

# Convert
go run . --in mydb.sql --out mydb.db --overwrite
```

Flags:

| Flag          | Required | Meaning                                                          |
|---------------|----------|------------------------------------------------------------------|
| `--in`        | yes      | `pg_dump` file, or `-` to read stdin                             |
| `--out`       | yes      | path of the SQLite database to create                            |
| `--schema`    | no       | SQLite database the app created, whose schema the output follows |
| `--keep`      | no       | table whose rows stay as the reference database has them, repeatable |
| `--overwrite` | no       | replace an existing output file (and its `-wal`/`-shm` sidecars) |
| `--strict`    | no       | fail instead of warning when part of the schema cannot be kept    |
| `--json`      | no       | log structured JSON instead of readable text                     |
| `--verbose`   | no       | log per-table and per-skipped-statement detail                   |
| `--quiet`     | no       | log only errors                                                  |

Without `--overwrite` the tool refuses to touch an existing file, because the
output is the whole database. `--strict` is for automated runs: any warning
becomes a non-zero exit, so a half-migrated schema cannot pass silently.

A custom-format archive (`pg_dump -Fc`) is accepted too and piped through
`pg_restore`. The binary is looked up before anything is written: when it is
missing the tool stops with instructions instead of producing an empty database.

The database is built in a temporary file beside `--out` and moved into place
only once it is complete, so a failed or interrupted run leaves nothing behind
and never damages the database it was replacing.

Logs go to stderr, so stdout stays free for callers. A successful run is one
line:

```
converted 877 rows into 34 tables and 36 indexes in 27ms -> mydb.db
```

Warnings (dropped defaults, constraints SQLite refused) are printed as they
happen; `--verbose` adds a line per table and per skipped statement. `--json`
switches every line to a structured record for `jq`:

```bash
pg2sqlite --in mydb.sql --out mydb.db --json 2>&1 >/dev/null | jq -r 'select(.msg=="converted") | .rows'
```

On a terminal, a long run reports its row count every couple of seconds; piped
output stays silent. `pg2sqlite` with no arguments and `--help` both print on
stdout and exit `0`. Shell completion comes from the `completion` subcommand
(`pg2sqlite completion bash|zsh|fish|powershell`).

## Following the app's own schema

An app that ships separate PostgreSQL and SQLite migrations does not translate
one schema into the other: the SQLite one is written on its own terms, with
column collations, `BOOLEAN` declarations, `STRICT`/`WITHOUT ROWID` tables, and
timestamps kept as epoch integers. Deriving the tables from `pg_dump` then
produces a database the app cannot read.

`--schema` takes a database the app itself created, and starts the output as a
copy of it, so the tables, column types, collations, CHECK constraints, indexes,
triggers and views are exactly the ones the app installed. The rows the copy
carries are discarded and the dump supplies every row:

```bash
# Let the app create its schema on an empty data directory, stop it, then:
go run . --in mydb.sql --out mydb.db --overwrite --schema scratch-data/app.db
```

Rows are converted per the target column type rather than the source one: a
`DATETIME` column gets the driver's own layout, a `date` column stays date-only,
an `integer` column gets unix milliseconds, an interval becomes milliseconds, and
a boolean becomes `0`/`1`. Generated columns are left to the app, columns the
reference does not have are dropped with a warning, and tables it does not have
keep no rows.

The output is left out of write-ahead mode, so the finished database is a single
file.

Some apps keep a migration history per backend rather than one shared history,
so the rows in Postgres describe migrations SQLite has never seen. `--keep` names
such tables: the reference database's rows stay, and the dump contributes
nothing to them.

```bash
go run . --in mydb.sql --out mydb.db --schema scratch-data/app.db \
  --keep __diesel_schema_migrations
```

## Exit codes

`0` success, `1` conversion failure, `2` bad usage, `4` input not found, `5`
output exists without `--overwrite`, `130`/`143` interrupted by `SIGINT`/
`SIGTERM`. Under `--json` the failure is a record on stderr:

```json
{"error":{"code":"CONFLICT","exit_code":5,"message":"mydb.db already exists","run":"pg2sqlite --overwrite"}}
```

## What is supported

- `CREATE TABLE`, including multi-line column lists, quoted identifiers,
  schema-qualified names, `public.<enum>` column types, `IF NOT EXISTS` and the
  `PARTITION BY` tail.
- **Partitioned tables**: `PARTITION OF` children fold their rows into the
  parent, which is what SQLite's single table wants.
- **Multiple schemas**: tables outside `public` are created as `<schema>__<table>`,
  so two schemas holding the same table name stay distinct.
- Primary keys (inline or `ALTER TABLE ... ADD CONSTRAINT`), `UNIQUE` and
  `CHECK` constraints. When SQLite rejects a constraint the table is rebuilt
  without it and the loss is reported rather than failing the run.
- `CREATE [UNIQUE] INDEX ... USING btree (...)` with optional `WHERE`
  predicates: operator classes (`varchar_pattern_ops`) are dropped,
  `= ANY (ARRAY[...])` becomes `IN (...)`, `col #>> '{a,b}'` becomes
  `json_extract(col, '$.a.b')` and a trailing `AT TIME ZONE` is dropped.
- `CREATE VIEW` is attempted after the tables are built; a view SQLite cannot
  express is reported and skipped. The duplicate `CREATE OR REPLACE VIEW` that
  pg_dump emits for a view's `_RETURN` rule is not replayed.
- Rows from both `COPY ... FROM stdin` (any line length) and
  `--inserts`/`--column-inserts` dumps, including the `\N` NULL marker,
  string escapes and `bytea` in hex or escape form.

Everything else (`SET`, `SELECT pg_catalog.set_config`, `CREATE SEQUENCE`,
`CREATE TYPE`, `CREATE TRIGGER`, `CREATE FUNCTION`, `ALTER ... OWNER TO`,
`COMMENT`, `GRANT`, `\restrict`) is counted and reported: it carries no table
data, and SQLite has no equivalent object.

## Type mapping

| PostgreSQL                                                                                         | SQLite    |
|----------------------------------------------------------------------------------------------------|-----------|
| `smallint`, `integer`, `bigint`, `serial`, `bigserial`, `oid`, `boolean`                           | `INTEGER` |
| `real`, `double precision`                                                                         | `REAL`    |
| `numeric`, `decimal`, `money`                                                                      | `NUMERIC` |
| `bytea`                                                                                            | `BLOB`    |
| everything else (`text`, `varchar`, `uuid`, `json`, `jsonb`, `timestamptz`, `inet`, enums, arrays) | `TEXT`    |

`timestamptz` keeps PostgreSQL's textual form (`2024-08-21 17:54:04.853+00`),
so comparisons in SQLite stay lexicographic and therefore chronological.
Booleans become `1`/`0`, and `bytea` hex (`\x0102`) becomes a real BLOB.

Defaults are carried over with casts stripped (`''::character varying` → `''`,
`now()` → `CURRENT_TIMESTAMP`, `true` → `1`). PostgreSQL clock helpers collapse
to `CURRENT_TIMESTAMP`: `timezone('utc'::text, now())` and
`(now() AT TIME ZONE 'utc'::text)` both become `CURRENT_TIMESTAMP`.

Defaults SQLite cannot reproduce are dropped: `nextval(...)` sequence lookups,
client-side generators such as `gen_random_uuid()`, and any other function call,
including schema-qualified ones like `public.custom_uuid()`. They are reported
once per table. The app must supply those values itself, so check that the
consuming app creates its own primary keys and timestamps on SQLite. Dropped
defaults on an integer primary key are not reported: SQLite assigns those from
the rowid anyway.

`timestamptz` values are normalised to UTC (`11:00+02` becomes `09:00+00`), so
text ordering stays chronological even when the dump was taken in another
session timezone.

## Limits

- **Generated columns arrive empty.** `pg_dump` exports no value for a
  `GENERATED ALWAYS AS (...) STORED` column and leaves it out of `COPY`, so the
  column exists but is NULL. Each one is logged at `WARN`.
- **Decimals are floats.** SQLite's `NUMERIC` is a float, so a
  `numeric(30,10)` value with more than ~15 significant digits rounds. Keeping
  exact decimals means storing them as text, which the consuming app has to
  agree to.
- **`inet`/`cidr` lose their netmask.** The dump itself writes `10.244.0.1`
  where `remote_addr::text` renders `10.244.0.1/32`; the address is identical.
- **No foreign keys, triggers, functions or sequences.** `pg_dump` emits them as
  separate statements which are skipped and counted. SQLite cannot express
  plpgsql, and its sequences are implicit in `INTEGER PRIMARY KEY`.
- **Views are best effort.** `LATERAL` joins and PostgreSQL-only functions in a
  view body are reported and the view is skipped.
- **`date_trunc(...)` index expressions are skipped**, since SQLite has no
  equivalent and a wrong index would be worse than none.
- **A truncated dump is rejected.** If a `COPY` block or a statement ends
  early — a dropped `kubectl exec` stream, a killed `pg_dump` — the run fails
  and nothing is committed, rather than leaving a partially loaded database.
- **A row with the wrong number of values is rejected.** A `COPY` or `VALUES`
  row whose field count disagrees with its column list means the dump is
  damaged; inserting it misaligned would silently shift every column.
- **The source is read twice**, so it must be seekable. stdin and `pg_restore`
  output are copied to a temporary file on disk first (never into memory) and
  removed afterwards.

## Performance

Rows are streamed: the dump is read once for the schema and once for the data,
and every row goes straight into a prepared statement inside a single
transaction. Peak memory stays flat as the dump grows, because no table's rows
are ever held in memory.

SQLite allows a single writer, so there is nothing to parallelise per database;
converting several dumps concurrently means running several processes.

## Development

```bash
go test ./...                              # unit tests, no external services
golangci-lint run --fix ./...
staticcheck ./...

# fuzzing: the parser, then the whole pipeline
go test -run '^$' -fuzz '^FuzzParseDump$' -fuzztime 60s
go test -run '^$' -fuzz '^FuzzConvert$' -fuzztime 60s
```

`FuzzParseDump` checks that parsing is deterministic and self-consistent;
`FuzzConvert` checks that no parsed row is lost, miscounted or misplaced on its
way into SQLite. Inputs that once failed live in `testdata/fuzz/` and replay on
every `go test`.

Behaviour is checked against real dumps by comparing per-table row counts,
per-column text lengths and per-column BLOB byte totals with the source
database, and by running the target application against the converted file.

