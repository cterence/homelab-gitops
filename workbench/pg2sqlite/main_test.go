package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name   string
		asJSON bool
		level  slog.Level
		want   []string
		absent []string
	}{
		{
			name:   "json is machine readable",
			asJSON: true,
			level:  slog.LevelInfo,
			want:   []string{`"msg":"converted"`, `"rows":3`},
		},
		{
			name:   "text is readable and undated",
			level:  slog.LevelInfo,
			want:   []string{"msg=converted", "rows=3"},
			absent: []string{"time=", `"msg"`},
		},
		{
			name:   "per-table detail is hidden at info",
			level:  slog.LevelInfo,
			absent: []string{"detail"},
		},
		{
			name:  "debug shows per-table detail",
			level: slog.LevelDebug,
			want:  []string{"detail"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			logger := newLogger(&buf, tt.asJSON, tt.level)
			logger.Debug("table", "detail", "yes")
			logger.Info("converted", "rows", 3)

			out := buf.String()

			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output %q does not contain %q", out, want)
				}
			}

			for _, absent := range tt.absent {
				if strings.Contains(out, absent) {
					t.Errorf("output %q contains %q", out, absent)
				}
			}

			if !tt.asJSON {
				return
			}

			for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
				if !json.Valid([]byte(line)) {
					t.Errorf("line %q is not valid JSON", line)
				}
			}
		})
	}
}

func TestLogLevel(t *testing.T) {
	tests := []struct {
		name string
		cfg  config
		want slog.Level
	}{
		{name: "default", cfg: config{}, want: slog.LevelInfo},
		{name: "quiet", cfg: config{quiet: true}, want: slog.LevelError},
		{name: "verbose wins over quiet", cfg: config{quiet: true, verbose: true}, want: slog.LevelDebug},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logLevel(tt.cfg); got != tt.want {
				t.Errorf("logLevel = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReportError(t *testing.T) {
	tests := []struct {
		name  string
		cfg   config
		err   error
		sig   os.Signal
		code  int
		want  string
		keyed map[string]any
	}{
		{
			name: "usage is code 2 with the fix",
			err:  usageError(errors.New(`Required flags "in, out" not set`)),
			code: 2,
			want: "run: pg2sqlite --help",
		},
		{
			name: "missing input is code 4",
			err:  notFoundError(errors.New("opening dump.sql: no such file or directory")),
			code: 4,
		},
		{
			name: "existing output is code 5",
			err:  conflictError(errors.New("out.db already exists")),
			code: 5,
			want: "run: pg2sqlite --overwrite",
		},
		{
			name: "runtime failure is code 1",
			err:  errors.New("truncated dump"),
			code: 1,
		},
		{
			name: "cancel is an interrupt",
			err:  context.Canceled,
			code: 130,
			want: "pg2sqlite: interrupted",
		},
		{
			name: "sigterm is code 143",
			err:  context.Canceled,
			sig:  syscall.SIGTERM,
			code: 143,
		},
		{
			name: "json carries the code and the fix",
			cfg:  config{json: true},
			err:  usageError(errors.New("flag provided but not defined: -nope")),
			code: 2,
			keyed: map[string]any{
				"code": "USAGE", "exit_code": float64(2), "run": "pg2sqlite --help",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			if got := reportError(&buf, &tt.cfg, tt.err, tt.sig); got != tt.code {
				t.Errorf("exit code = %d, want %d", got, tt.code)
			}

			if tt.want != "" && !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output %q does not contain %q", buf.String(), tt.want)
			}

			if tt.keyed == nil {
				return
			}

			var payload struct {
				Error map[string]any `json:"error"`
			}

			if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
				t.Fatalf("output %q is not JSON: %v", buf.String(), err)
			}

			for key, want := range tt.keyed {
				if payload.Error[key] != want {
					t.Errorf("error.%s = %v, want %v", key, payload.Error[key], want)
				}
			}
		})
	}
}

func TestPrepareOutputRejectsExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.db")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	err := prepareOutput(config{out: path})
	if err == nil {
		t.Fatal("expected an existing database to be refused")
	}

	if got := exitCode(err); got != 5 {
		t.Errorf("exit code = %d, want 5", got)
	}

	kept, err := os.ReadFile(path)
	if err != nil || string(kept) != "keep me" {
		t.Errorf("the existing database was touched: %q %v", kept, err)
	}
}

// A failed run must leave the directory exactly as it found it: no half-built
// database and no journal beside it.
func TestConvertFileLeavesNoOutputOnFailure(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "dump.sql")

	truncated := "CREATE TABLE public.t (id integer NOT NULL);\nCOPY public.t (id) FROM stdin;\n1\n"
	if err := os.WriteFile(in, []byte(truncated), 0o600); err != nil {
		t.Fatalf("writing dump: %v", err)
	}

	if err := convertFile(context.Background(), config{in: in, out: filepath.Join(dir, "out.db")}); err == nil {
		t.Fatal("expected a truncated dump to fail")
	}

	assertDir(t, dir, "dump.sql")
}

func TestConvertFileWritesThroughTempFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "dump.sql")

	dump := "CREATE TABLE public.t (id integer NOT NULL, name text);\nCOPY public.t (id, name) FROM stdin;\n1\talpha\n\\.\n"
	if err := os.WriteFile(in, []byte(dump), 0o600); err != nil {
		t.Fatalf("writing dump: %v", err)
	}

	if err := convertFile(context.Background(), config{in: in, out: filepath.Join(dir, "out.db")}); err != nil {
		t.Fatalf("convertFile: %v", err)
	}

	assertDir(t, dir, "dump.sql", "out.db")
}

func assertDir(t *testing.T, dir string, want ...string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}

	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("directory holds %v, want %v", got, want)
	}
}
