// pg2sqlite converts a pg_dump artifact into a SQLite database, so an app can
// move from PostgreSQL to SQLite.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cli "github.com/urfave/cli/v3"
)

// pgDumpMagic is the first bytes of a custom-format (-Fc) archive.
const pgDumpMagic = "PGDMP"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "pg2sqlite: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	in        string
	out       string
	overwrite bool
	strict    bool
}

func run(ctx context.Context, args []string) error {
	var cfg config

	cmd := &cli.Command{
		Name:  "pg2sqlite",
		Usage: "convert a pg_dump artifact into a SQLite database",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "in", Required: true, Destination: &cfg.in, Usage: "pg_dump file, or - for stdin"},
			&cli.StringFlag{Name: "out", Required: true, Destination: &cfg.out, Usage: "path of the SQLite database to create"},
			&cli.BoolFlag{Name: "overwrite", Destination: &cfg.overwrite, Usage: "replace the output database if it already exists"},
			&cli.BoolFlag{Name: "strict", Destination: &cfg.strict, Usage: "fail instead of warning when part of the schema cannot be reproduced"},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			return convertFile(ctx, cfg)
		},
	}

	return cmd.Run(ctx, args)
}

// convertFile is the command body: everything after flag parsing.
func convertFile(ctx context.Context, cfg config) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := prepareOutput(cfg); err != nil {
		return err
	}

	source, err := openSource(ctx, cfg.in)
	if err != nil {
		return err
	}

	defer func() { _ = source.Close() }()

	db, err := sql.Open("sqlite", cfg.out)
	if err != nil {
		return fmt.Errorf("opening %s: %w", cfg.out, err)
	}

	defer func() { _ = db.Close() }()

	started := time.Now()

	result, err := convert(ctx, source.file, db, cfg.strict)
	if err != nil {
		return err
	}

	for _, count := range result.tables {
		slog.Info("table", "name", count.table, "rows", count.rows)
	}

	for kind, count := range result.skipped {
		slog.Info("skipped statement", "kind", kind, "count", count)
	}

	slog.Info("converted",
		"tables", len(result.tables),
		"rows", result.rows,
		"indexes", result.indexes,
		"skipped_indexes", result.skippedIndexes,
		"views", result.views,
		"skipped_views", result.skippedViews,
		"warnings", result.warnings,
		"out", cfg.out,
		"duration", time.Since(started).Round(time.Millisecond).String())

	return nil
}

// prepareOutput refuses to overwrite an existing database unless asked, and
// otherwise removes it along with any stale write-ahead log sidecars — a
// leftover -wal would be replayed into the new database.
func prepareOutput(cfg config) error {
	if !cfg.overwrite {
		_, err := os.Stat(cfg.out)
		switch {
		case err == nil:
			return fmt.Errorf("%s already exists (pass --overwrite to replace it)", cfg.out)
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("checking %s: %w", cfg.out, err)
		}

		return nil
	}

	for _, path := range []string{cfg.out, cfg.out + "-wal", cfg.out + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", path, err)
		}
	}

	return nil
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
			return nil, fmt.Errorf("opening %s: %w", path, err)
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
