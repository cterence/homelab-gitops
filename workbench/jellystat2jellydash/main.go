// jellystat2jellydash converts a Jellystat JSONL backup into a Jellydash History
// CSV, so viewing history survives a move between the two dashboards.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	cli "github.com/urfave/cli/v3"
)

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
	backup   string
	output   string
	timezone string
	json     bool
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
	return &cliError{code: "USAGE", exit: 2, run: "jellystat2jellydash --help", err: err}
}

func notFoundError(err error) error {
	return &cliError{code: "NOT_FOUND", exit: 4, err: err}
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

	_, _ = fmt.Fprintf(w, "jellystat2jellydash: %s\n", message)

	if run != "" {
		_, _ = fmt.Fprintf(w, "run: %s\n", run)
	}

	return code
}

func run(ctx context.Context, args []string, cfg *config) error {
	cmd := &cli.Command{
		Name:      "jellystat2jellydash",
		Usage:     "convert a Jellystat backup into a Jellydash History CSV",
		UsageText: "jellystat2jellydash <backup.jsonl> <output.csv> [--timezone <IANA name>] [--json]",
		Description: "Reads the JSONL backup Jellystat writes from its database and emits the CSV that\n" +
			"Jellydash imports from Settings > Import play history (format version 2).\n" +
			"\n" +
			"Examples:\n" +
			"   jellystat2jellydash backup.jsonl history.csv\n" +
			"   jellystat2jellydash backup.jsonl history.csv --timezone Europe/Paris\n" +
			"   jellystat2jellydash backup.jsonl history.csv --json | jq -r '.msg'",
		Version:               buildVersion(),
		EnableShellCompletion: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "timezone", Destination: &cfg.timezone, Usage: "IANA timezone the timestamps are written in and read back as (default: $TZ, else the local zone)"},
			&cli.BoolFlag{Name: "json", Destination: &cfg.json, Usage: "log structured JSON instead of text"},
		},
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "backup", UsageText: "Jellystat JSONL backup", Destination: &cfg.backup},
			&cli.StringArg{Name: "output", UsageText: "Jellydash History CSV to write", Destination: &cfg.output},
		},
		OnUsageError: func(_ context.Context, _ *cli.Command, err error, _ bool) error {
			return usageError(err)
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Destination arguments are consumed from the positional list, so
			// anything left over is an argument this command does not take.
			if extra := cmd.Args().First(); extra != "" {
				return usageError(fmt.Errorf("unexpected argument %q", extra))
			}

			if cfg.backup == "" || cfg.output == "" {
				return usageError(errors.New("expected a backup and an output path"))
			}

			return convert(ctx, *cfg)
		},
	}

	// Bare invocation asks for help rather than reporting missing arguments.
	if len(args) <= 1 {
		args = []string{cmd.Name, "--help"}
	}

	return cmd.Run(ctx, args)
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return "dev"
}

func newLogger(w io.Writer, asJSON bool) *slog.Logger {
	if asJSON {
		return slog.New(slog.NewJSONHandler(w, nil))
	}

	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}

			return a
		},
	}))
}

// resolveZone turns the requested timezone into a location. An empty value
// follows $TZ, then the host zone. Jellydash rejects a zone it cannot name, so
// a host without one is a usage error rather than a silent shift.
func resolveZone(name string) (*time.Location, error) {
	if name == "" {
		name = os.Getenv("TZ")
	}

	if name == "" || name == "Local" {
		name = time.Local.String()
	}

	if name == "" || name == "Local" {
		return nil, usageError(errors.New("cannot name the local timezone: pass --timezone <IANA name> or set TZ"))
	}

	zone, err := time.LoadLocation(name)
	if err != nil {
		return nil, usageError(fmt.Errorf("unknown timezone %q: pass an IANA name such as Europe/Paris", name))
	}

	return zone, nil
}
