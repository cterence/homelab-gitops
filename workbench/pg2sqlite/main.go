// pg2sqlite converts a pg_dump artifact into a SQLite database, so an app can
// move from PostgreSQL to SQLite.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	cli "github.com/urfave/cli/v3"
)

// pgDumpMagic is the first bytes of a custom-format (-Fc) archive.
const pgDumpMagic = "PGDMP"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan os.Signal, 1)

	sigs := make(chan os.Signal, 1)

	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	go func() {
		if sig, ok := <-sigs; ok {
			received <- sig

			cancel()
		}
	}()

	cfg := &config{}

	if err := run(ctx, os.Args, cfg); err != nil {
		var sig os.Signal

		select {
		case sig = <-received:
		default:
		}

		os.Exit(reportError(os.Stderr, cfg, err, sig))
	}
}

type config struct {
	in        string
	out       string
	overwrite bool
	strict    bool
	json      bool
	verbose   bool
	quiet     bool
}

// cliError carries a semantic exit code and the command that fixes the
// problem, so a script can branch without parsing the message.
type cliError struct {
	code string
	exit int
	run  string
	err  error
}

func (e *cliError) Error() string { return e.err.Error() }

func (e *cliError) Unwrap() error { return e.err }

func usageError(err error) error {
	return &cliError{code: "USAGE", exit: 2, run: "pg2sqlite --help", err: err}
}

func notFoundError(err error) error {
	return &cliError{code: "NOT_FOUND", exit: 4, err: err}
}

func conflictError(err error) error {
	return &cliError{code: "CONFLICT", exit: 5, run: "pg2sqlite --overwrite", err: err}
}

func exitCode(err error) int {
	var coded *cliError
	if errors.As(err, &coded) {
		return coded.exit
	}

	return 1
}

// reportError prints one line on stderr, in the same shape as the log, and
// returns the exit code for the caller.
func reportError(w io.Writer, cfg *config, err error, sig os.Signal) int {
	code := exitCode(err)

	var coded *cliError

	run := ""
	if errors.As(err, &coded) {
		run = coded.run
	}

	message := err.Error()

	if errors.Is(err, context.Canceled) || sig != nil {
		code = 130
		message = "interrupted"

		if sig == syscall.SIGTERM {
			code = 143
		}
	}

	if cfg.json {
		payload := map[string]any{"message": message, "exit_code": code}
		if coded != nil {
			payload["code"] = coded.code
		}

		if run != "" {
			payload["run"] = run
		}

		if encoded, marshalErr := json.Marshal(map[string]any{"error": payload}); marshalErr == nil {
			_, _ = fmt.Fprintf(w, "%s\n", encoded)
		}

		return code
	}

	_, _ = fmt.Fprintf(w, "pg2sqlite: %s\n", message)

	if run != "" {
		_, _ = fmt.Fprintf(w, "run: %s\n", run)
	}

	return code
}

func run(ctx context.Context, args []string, cfg *config) error {
	cmd := &cli.Command{
		Name:  "pg2sqlite",
		Usage: "convert a pg_dump artifact into a SQLite database",
		UsageText: "pg2sqlite --in <dump.sql|-> --out <database> [--overwrite] [--strict]\n" +
			"                  [--json] [--verbose] [--quiet]",
		Description: "Reads a plain-format pg_dump (or a custom-format archive when pg_restore is\n" +
			"on PATH) and writes a SQLite database, streaming the rows so memory stays flat.\n" +
			"\n" +
			"Examples:\n" +
			"   pg2sqlite --in mydb.sql --out mydb.db\n" +
			"   pg_dump -U postgres -d mydb | pg2sqlite --in - --out mydb.db --overwrite\n" +
			"   pg2sqlite --in mydb.sql --out mydb.db --json | jq -r 'select(.msg==\"converted\") | .rows'",
		Version:               buildVersion(),
		EnableShellCompletion: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "in", Required: true, Destination: &cfg.in, Usage: "pg_dump file, or - for stdin"},
			&cli.StringFlag{Name: "out", Required: true, Destination: &cfg.out, Usage: "path of the SQLite database to create"},
			&cli.BoolFlag{Name: "overwrite", Destination: &cfg.overwrite, Usage: "replace the output database if it already exists"},
			&cli.BoolFlag{Name: "strict", Destination: &cfg.strict, Usage: "fail instead of warning when part of the schema cannot be reproduced"},
			&cli.BoolFlag{Name: "json", Destination: &cfg.json, Usage: "log structured JSON instead of text"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Destination: &cfg.verbose, Usage: "log per-table and per-skipped-statement detail, wins over --quiet"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Destination: &cfg.quiet, Usage: "log only errors"},
		},
		OnUsageError: func(_ context.Context, _ *cli.Command, err error, _ bool) error {
			return usageError(err)
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			return convertFile(ctx, *cfg)
		},
	}

	// Bare invocation asks for help rather than reporting two missing flags.
	if len(args) <= 1 {
		args = []string{cmd.Name, "--help"}
	}

	return cmd.Run(ctx, args)
}

// buildVersion reports the module version, or "dev" for a local build.
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return "dev"
}

// newLogger writes diagnostics to w: JSON for machines, readable text
// otherwise. The timestamp is dropped from the text form because a run lasts
// seconds.
func newLogger(w io.Writer, asJSON bool, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if asJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}

	opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}

		return a
	}

	return slog.New(slog.NewTextHandler(w, opts))
}

// logLevel maps the verbosity flags onto a level, with --verbose winning over
// --quiet.
func logLevel(cfg config) slog.Level {
	switch {
	case cfg.verbose:
		return slog.LevelDebug
	case cfg.quiet:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// convertFile is the command body: everything after flag parsing.
func convertFile(ctx context.Context, cfg config) error {
	slog.SetDefault(newLogger(os.Stderr, cfg.json, logLevel(cfg)))

	if err := prepareOutput(cfg); err != nil {
		return err
	}

	source, err := openSource(ctx, cfg.in)
	if err != nil {
		return err
	}

	defer func() { _ = source.Close() }()

	out, err := newOutput(cfg.out)
	if err != nil {
		return err
	}

	defer out.discard()

	db, err := sql.Open("sqlite", out.temp)
	if err != nil {
		return fmt.Errorf("opening %s: %w", cfg.out, err)
	}

	defer func() { _ = db.Close() }()

	started := time.Now()

	result, err := convert(ctx, source.file, db, cfg.strict, progressReporter(cfg))
	if err != nil {
		return err
	}

	// Close before the rename so no handle points at the finished file.
	if err := db.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", cfg.out, err)
	}

	if err := out.commit(); err != nil {
		return err
	}

	for _, count := range result.tables {
		slog.Debug("table", "name", count.table, "rows", count.rows)
	}

	for kind, count := range result.skipped {
		slog.Debug("skipped statement", "kind", kind, "count", count)
	}

	if cfg.quiet {
		return nil
	}

	duration := time.Since(started).Round(time.Millisecond)

	if cfg.json {
		slog.Info("converted",
			"tables", len(result.tables),
			"rows", result.rows,
			"indexes", result.indexes,
			"skipped_indexes", result.skippedIndexes,
			"views", result.views,
			"skipped_views", result.skippedViews,
			"warnings", result.warnings,
			"out", cfg.out,
			"duration", duration.String())

		return nil
	}

	fmt.Fprintf(os.Stderr, "converted %d rows into %d tables and %d indexes in %s -> %s\n",
		result.rows, len(result.tables), result.indexes, duration, cfg.out)

	return nil
}

// prepareOutput refuses to overwrite an existing database unless asked, and
// otherwise removes any journal or write-ahead log left beside it — a stale
// -wal would be replayed into the new database.
func prepareOutput(cfg config) error {
	_, err := os.Stat(cfg.out)

	switch {
	case err == nil && !cfg.overwrite:
		return conflictError(fmt.Errorf("%s already exists", cfg.out))
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("checking %s: %w", cfg.out, err)
	}

	for _, path := range []string{cfg.out + "-journal", cfg.out + "-wal", cfg.out + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", path, err)
		}
	}

	return nil
}

// output builds the database beside its destination and moves it into place
// only on success, so a failed or interrupted run leaves nothing behind and
// never damages a database it was told to overwrite.
type output struct {
	path string
	temp string
}

func newOutput(path string) (*output, error) {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("creating a temporary database beside %s: %w", path, err)
	}

	name := file.Name()

	if err := file.Close(); err != nil {
		_ = os.Remove(name)

		return nil, fmt.Errorf("closing %s: %w", name, err)
	}

	return &output{path: path, temp: name}, nil
}

func (o *output) commit() error {
	if err := os.Rename(o.temp, o.path); err != nil {
		return fmt.Errorf("moving the database into %s: %w", o.path, err)
	}

	o.temp = ""

	return nil
}

func (o *output) discard() {
	if o.temp == "" {
		return
	}

	for _, path := range []string{o.temp, o.temp + "-journal", o.temp + "-wal", o.temp + "-shm"} {
		_ = os.Remove(path)
	}
}

// progressReporter prints a row count every couple of seconds, and only to a
// terminal: a run with no output for minutes looks hung, but a pipe has no use
// for it.
func progressReporter(cfg config) func(rows int) {
	if cfg.json || cfg.quiet || !isTerminal(os.Stderr) {
		return nil
	}

	last := time.Now()

	return func(rows int) {
		now := time.Now()
		if now.Sub(last) < 2*time.Second {
			return
		}

		last = now

		fmt.Fprintf(os.Stderr, "... %d rows\n", rows)
	}
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// source is the dump as a rewound file on disk. Streaming needs two passes, so
// stdin and pg_restore output are spooled rather than piped.
type source struct {
	file *os.File
	temp string
}

func (s *source) Close() error {
	err := s.file.Close()

	if s.temp != "" {
		if removeErr := os.Remove(s.temp); removeErr != nil && err == nil {
			err = removeErr
		}
	}

	return err
}

func openSource(ctx context.Context, path string) (*source, error) {
	var (
		file *os.File
		temp string
		err  error
	)

	if path == "-" {
		file, temp, err = spool(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("spooling stdin: %w", err)
		}
	} else {
		file, err = os.Open(path)
		if err != nil {
			return nil, notFoundError(fmt.Errorf("opening %s: %w", path, err))
		}
	}

	archive, err := isCustomFormat(file)
	if err != nil {
		_ = file.Close()

		removeTemp(temp)

		return nil, err
	}

	if !archive {
		return &source{file: file, temp: temp}, nil
	}

	name := path
	if name == "-" {
		name = "stdin"
	}

	restored, err := restoreToTemp(ctx, file, name)
	_ = file.Close()

	removeTemp(temp)

	return restored, err
}

func isCustomFormat(file *os.File) (bool, error) {
	head := make([]byte, len(pgDumpMagic))

	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, fmt.Errorf("reading %s: %w", file.Name(), err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("rewinding %s: %w", file.Name(), err)
	}

	return string(head[:n]) == pgDumpMagic, nil
}

// spool copies a stream to a temporary file and returns it rewound.
func spool(r io.Reader) (*os.File, string, error) {
	file, err := os.CreateTemp("", "pg2sqlite-*.sql")
	if err != nil {
		return nil, "", err
	}

	if _, err := io.Copy(file, r); err != nil {
		_ = file.Close()
		removeTemp(file.Name())

		return nil, "", err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		removeTemp(file.Name())

		return nil, "", err
	}

	return file, file.Name(), nil
}

// restoreToTemp expands a custom-format archive through pg_restore. The binary
// is looked up first so a missing tool gives an actionable message instead of a
// half-converted database.
func restoreToTemp(ctx context.Context, archive *os.File, name string) (*source, error) {
	binary, err := exec.LookPath("pg_restore")
	if err != nil {
		return nil, fmt.Errorf("%s is a custom-format archive and pg_restore is not on PATH: run `pg_restore -f dump.sql %s` first", name, name)
	}

	out, temp, err := spool(strings.NewReader(""))
	if err != nil {
		return nil, fmt.Errorf("creating temporary file: %w", err)
	}

	cmd := exec.CommandContext(ctx, binary, "--no-owner", "--no-privileges", "--file", "-")
	cmd.Stdin = archive
	cmd.Stdout = out

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		_ = out.Close()

		removeTemp(temp)

		return nil, fmt.Errorf("pg_restore: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	if _, err := out.Seek(0, io.SeekStart); err != nil {
		_ = out.Close()

		removeTemp(temp)

		return nil, fmt.Errorf("rewinding pg_restore output: %w", err)
	}

	return &source{file: out, temp: temp}, nil
}

func removeTemp(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
