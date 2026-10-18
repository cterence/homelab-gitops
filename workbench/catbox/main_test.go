package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runCLI re-execs the test binary in helper mode (see
// integration_test.go) with an isolated HOME, so the real run()
// runs end to end with no network.
func runCLI(t *testing.T, args ...string) (out string, code int) {
	t.Helper()

	cmd := exec.Command(os.Args[0], args...)

	cmd.Env = append(os.Environ(), "CATBOX_HELPER=1", "HOME="+t.TempDir())

	var buf bytes.Buffer

	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running catbox %v: %v", args, err)
		}

		code = exit.ExitCode()
	}

	return buf.String(), code
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"usage", fmt.Errorf("%w: join takes <name>", errUsage), 2},
		{"not found", fmt.Errorf("%w: no such pending item: abc", errNotFound), 4},
		{"interrupted", context.Canceled, 130},
		{"runtime", errors.New("dial tcp: connection refused"), 1},
		{"json-wrapped usage", &jsonError{fmt.Errorf("%w: oops", errUsage)}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Fatalf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestHelpExitsZero(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"bare", nil},
		{"help", []string{"help"}},
		{"-h", []string{"-h"}},
		{"--help", []string{"--help"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code := runCLI(t, tt.args...)
			if code != 0 {
				t.Fatalf("catbox %v: exit = %d, want 0", tt.args, code)
			}

			if !strings.Contains(out, "usage:") {
				t.Fatalf("catbox %v: no usage in output: %q", tt.args, out)
			}
		})
	}
}

func TestUnknownCommandExitsTwo(t *testing.T) {
	out, code := runCLI(t, "frobnicate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}

	if !strings.Contains(out, `unknown command "frobnicate"`) {
		t.Fatalf("no unknown-command error in output: %q", out)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"join missing token", []string{"join", "alice"}, "join takes"},
		{"rename missing name", []string{"rename"}, "rename takes"},
		{"send missing file", []string{"send", "bob"}, "send takes"},
		{"dismiss missing arg", []string{"dismiss"}, "dismiss takes"},
		{"remove missing arg", []string{"remove"}, "remove takes"},
		{"remove needs yes", []string{"remove", "bob"}, "--yes"},
		{"remove --data needs yes", []string{"remove", "--data", "/data", "bob"}, "--yes"},
		{"admin missing arg", []string{"admin"}, "admin takes"},
		{"reset needs yes", []string{"reset"}, "--yes"},
		{"bad max", []string{"serve", "--max", "wat"}, "--max"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code := runCLI(t, tt.args...)
			if code != 2 {
				t.Fatalf("catbox %v: exit = %d, want 2 (output: %q)", tt.args, code, out)
			}

			if !strings.Contains(out, tt.want) {
				t.Fatalf("catbox %v: %q not in output: %q", tt.args, tt.want, out)
			}
		})
	}
}

func TestStatusJSONErrorEnvelope(t *testing.T) {
	cmd := exec.Command(os.Args[0], "status", "--json")

	cmd.Env = append(os.Environ(), "CATBOX_HELPER=1", "HOME="+t.TempDir())

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("status --json: %v", err)
	}

	if exit.ExitCode() != 1 {
		t.Fatalf("exit = %d, want 1", exit.ExitCode())
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if !strings.Contains(stderr.String(), `"error"`) || !strings.Contains(stderr.String(), `"exit_code":1`) {
		t.Fatalf("no JSON error envelope on stderr: %q", stderr.String())
	}
}

func TestJSONEnvelope(t *testing.T) {
	got := string(jsonEnvelope(fmt.Errorf("%w: no such x", errNotFound)))
	want := `{"error":{"exit_code":4,"message":"not found: no such x"}}`

	if got != want {
		t.Fatalf("jsonEnvelope = %s, want %s", got, want)
	}
}

func TestBuildStamp(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"unset", "0", "unknown"},
		{"garbage", "dev", "unknown"},
		{"negative", "-1", "unknown"},
		{"baked", "1760000000", "9 Oct 2025, 08:53 UTC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buildTime = tt.in

			t.Cleanup(func() { buildTime = "0" })

			if got := buildStamp(); got != tt.want {
				t.Fatalf("buildStamp() with buildTime %q = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestVersionPrintsBuildStamp(t *testing.T) {
	out, code := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("catbox version: exit = %d, want 0", code)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "catbox ") || !strings.Contains(lines[0], "built ") {
		t.Fatalf("catbox version: want one line with version and build stamp, got: %q", out)
	}
}

func TestTokenFromStdin(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"trims newline", "tok123\n", "tok123"},
		{"trims spaces", "  tok123  \n", "tok123"},
		{"empty", "\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokenFromStdin(strings.NewReader(tt.in))
			if err != nil {
				t.Fatal(err)
			}

			if got != tt.want {
				t.Fatalf("tokenFromStdin(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
