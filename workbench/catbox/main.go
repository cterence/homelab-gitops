package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tailscale/tailcat"
)

// Baked at build time via -ldflags "-X main.version=…"; "dev" otherwise.
var version = "dev"

// Baked at build time via -ldflags "-X main.buildTime=…"; unix seconds,
// "0" in dev builds.
var buildTime = "0"

// buildStamp renders the baked build time; "unknown" when unset.
func buildStamp() string {
	sec, err := strconv.ParseInt(buildTime, 10, 64)
	if err != nil || sec <= 0 {
		return "unknown"
	}

	return time.Unix(sec, 0).UTC().Format("2 Jan 2006, 15:04 MST")
}

func main() {
	if err := run(); err != nil {
		fail(err)
	}
}

// errUsage marks an invalid invocation, errNotFound a named target
// that does not exist; exitCode maps both to their exit code.
var (
	errUsage    = errors.New("usage")
	errNotFound = errors.New("not found")
)

// jsonError marks a failure of a --json command: fail prints a JSON
// envelope on stderr instead of prose, so machine callers can parse
// failures too.
type jsonError struct{ err error }

func (e *jsonError) Error() string { return e.err.Error() }
func (e *jsonError) Unwrap() error { return e.err }

// exitCode maps an error to the exit code: 2 invalid invocation, 4
// target not found, 130 interrupted, 1 anything else.
func exitCode(err error) int {
	switch {
	case errors.Is(err, errUsage):
		return 2
	case errors.Is(err, errNotFound):
		return 4
	case errors.Is(err, context.Canceled):
		return 130
	default:
		return 1
	}
}

// fail prints err and exits: a JSON envelope when the command ran
// with --json, else one prose line.
func fail(err error) {
	code := exitCode(err)

	var je *jsonError
	if errors.As(err, &je) {
		fmt.Fprintf(os.Stderr, "%s\n", jsonEnvelope(err))
		os.Exit(code)
	}

	fmt.Fprintln(os.Stderr, "catbox:", err)
	os.Exit(code)
}

// jsonEnvelope renders err as the machine error object printed on
// stderr when --json was passed.
func jsonEnvelope(err error) []byte {
	env := struct {
		Error struct {
			ExitCode int    `json:"exit_code"`
			Message  string `json:"message"`
		} `json:"error"`
	}{}
	env.Error.ExitCode = exitCode(err)
	env.Error.Message = err.Error()

	b, _ := json.Marshal(env)

	return b
}

// tokenFromStdin reads the invite token (or bare stash address) from
// r, trimming surrounding whitespace.
func tokenFromStdin(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("reading token from stdin: %w", err)
	}

	return strings.TrimSpace(string(b)), nil
}

// newLogger logs to stderr: human text on a terminal, JSON in
// containers. CATBOX_LOG_TEXT forces text for the Android app's log
// pane.
func newLogger() *slog.Logger {
	if os.Getenv("CATBOX_LOG_TEXT") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	return slog.New(slog.NewJSONHandler(os.Stderr, nil))
}

func run() error {
	log := newLogger()

	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		usage(os.Stdout)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		data := fs.String("data", "", "data dir for identity, roster, and spool (defaults to CATBOX_DATA, else the host default)")
		region := fs.Int64("region", 0, "DERP region ID; 0 probes once on first boot")
		max := fs.String("max", "100G", "spool capacity cap")
		ttl := fs.Duration("ttl", 30*24*time.Hour, "spool retention")
		health := fs.String("health", ":8081", "health listen address")
		_ = fs.Parse(os.Args[2:])

		maxN, err := parseSize(*max)
		if err != nil {
			return fmt.Errorf("%w: --max: %v", errUsage, err)
		}

		dataDir, err := resolveDataDir(*data)
		if err != nil {
			return err
		}

		return runServe(ctx, log, dataDir, *region, maxN, *ttl, *health)
	case "addr":
		// Reads the same dir serve uses: --data is not offered, the
		// resolution matches (CATBOX_DATA, else the host default).
		dataDir, err := resolveDataDir("")
		if err != nil {
			return err
		}

		id, _, err := loadStashIdentity(ctx, dataDir, -1)
		if err != nil {
			return err
		}

		fmt.Println(id.Public.Addr())

		return nil
	case "join":
		fs := flag.NewFlagSet("join", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 2 {
			return fmt.Errorf("%w: join takes <name> <invite-token | stash-addr>", errUsage)
		}

		// The second argument is the invite token when it decodes,
		// else a bare stash address (the fresh-mesh bootstrap join).
		// "-" reads it from stdin, keeping the token out of the
		// shell history.
		name, addr, code := fs.Arg(0), "", ""

		arg := fs.Arg(1)
		if arg == "-" {
			tok, err := tokenFromStdin(os.Stdin)
			if err != nil {
				return err
			}

			if tok == "" {
				return fmt.Errorf("%w: empty token on stdin", errUsage)
			}

			arg = tok
		}

		if iv, err := parseInviteArg(arg); err == nil {
			addr, code = iv.Stash, iv.Code
		} else {
			addr = arg
		}

		created, err := runJoin(ctx, name, addr, code)
		if err != nil {
			return err
		}

		if created {
			fmt.Printf("joined as %s\n", name)
		} else {
			fmt.Printf("already joined as %s (roster refreshed)\n", name)
		}

		return nil
	case "rename":
		fs := flag.NewFlagSet("rename", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 1 {
			return fmt.Errorf("%w: rename takes <new-name>", errUsage)
		}

		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		release, err := lockPeer(ctx)
		if err != nil {
			return err
		}

		defer release()

		conn, cl, err := clientConn(ctx, id)
		if err != nil {
			return err
		}

		defer func() { _ = cl.Close() }()
		defer func() { _ = conn.Close() }()

		// The rename rides the join op: the stash validates the name
		// and retargets parked files. Stop listen first, or its
		// registered listener address goes stale.
		id.Name = fs.Arg(0)

		if err := joinReq(conn, id, "", ""); err != nil {
			return err
		}

		if err := saveJSON(filepath.Join(peerConfigDir(), "identity.json"), id); err != nil {
			return err
		}

		fmt.Printf("renamed to %s\n", id.Name)

		return nil
	case "recv":
		fs := flag.NewFlagSet("recv", flag.ExitOnError)
		dir := fs.String("dir", "", "inbox dir (default: OS downloads + /catbox)")
		_ = fs.Parse(os.Args[2:])

		if *dir == "" {
			var err error

			*dir, err = downloadDir()
			if err != nil {
				return err
			}
		}

		return runRecv(ctx, log, *dir, fs.Args())
	case "listen":
		fs := flag.NewFlagSet("listen", flag.ExitOnError)
		dir := fs.String("dir", "", "inbox dir (default: OS downloads + /catbox)")
		_ = fs.Parse(os.Args[2:])

		if *dir == "" {
			var err error

			*dir, err = downloadDir()
			if err != nil {
				return err
			}
		}

		return runListen(ctx, log, *dir)
	case "invite":
		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		release, err := lockPeer(ctx)
		if err != nil {
			return err
		}

		defer release()

		conn, cl, err := clientConn(ctx, id)
		if err != nil {
			return err
		}

		defer func() { _ = cl.Close() }()
		defer func() { _ = conn.Close() }()

		code, err := clientInvite(conn)
		if err != nil {
			return err
		}

		token, err := encodeInvite(inviteJSON{Code: code, Stash: string(id.StashAddr)})
		if err != nil {
			return err
		}

		fmt.Println(token)

		return nil
	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		jsonOut := fs.Bool("json", false, "machine-readable status for UIs")
		_ = fs.Parse(os.Args[2:])

		if err := runStatus(ctx, *jsonOut); err != nil {
			return &jsonError{err}
		}

		return nil
	case "send":
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		text := fs.String("text", "", "send this text as text-<date>.txt")
		positional := parseSendFlags(fs, os.Args[2:])

		target, srcs, err := sendSources(*text, positional)
		if err != nil {
			return err
		}

		for _, src := range srcs {
			if src.path == "" {
				continue
			}

			if fi, err := os.Stat(src.path); err != nil {
				return err
			} else if fi.IsDir() {
				return errors.New("send takes files, " + src.path + " is a directory")
			}
		}

		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		release, err := lockPeer(ctx)
		if err != nil {
			return err
		}

		defer release()

		var spooled []string

		defer func() {
			for _, dir := range spooled {
				_ = os.RemoveAll(dir)
			}
		}()

		for _, src := range srcs {
			path, label := src.path, src.path

			if path == "" {
				body := src.body

				if src.stdin {
					if body, err = io.ReadAll(os.Stdin); err != nil {
						return fmt.Errorf("reading stdin: %w", err)
					}
				}

				if path, err = spoolSend(body); err != nil {
					return err
				}

				spooled = append(spooled, filepath.Dir(path))
				label = filepath.Base(path)
			}

			// The SHA is known before sealing: it keys the
			// deterministic file secret and the receiver's resume
			// partial. The stash conn is clientSend's to dial, per
			// file: a long transfer starves its idle deadline, and
			// the next file must not inherit a dead conn.
			shaHex, err := fileSHA256(path)
			if err != nil {
				return fmt.Errorf("hashing %s: %w", path, err)
			}

			if err := clientSend(ctx, nil, id, target, path, shaHex); err != nil {
				return fmt.Errorf("sending %s: %w", label, err)
			}
		}

		return nil
	case "dismiss":
		fs := flag.NewFlagSet("dismiss", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 1 {
			return fmt.Errorf("%w: dismiss takes <id or file name> (see catbox status)", errUsage)
		}

		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		release, err := lockPeer(ctx)
		if err != nil {
			return err
		}

		defer release()

		conn, cl, err := clientConn(ctx, id)
		if err != nil {
			return err
		}

		defer func() { _ = cl.Close() }()
		defer func() { _ = conn.Close() }()

		// Resolve against this peer's own waiting items first: a
		// name must be unambiguous, and only ever one's own files.
		pend, err := clientPending(conn)
		if err != nil {
			return err
		}

		did, err := resolveDismiss(pend.Items, fs.Arg(0))
		if err != nil {
			return err
		}

		if err := clientDismiss(conn, did); err != nil {
			return err
		}

		fmt.Printf("dismissed %s\n", did)

		return nil
	case "remove":
		fs := flag.NewFlagSet("remove", flag.ExitOnError)
		data := fs.String("data", "", "stash data dir: run there to remove any member without being admin")
		yes := fs.Bool("yes", false, "confirm: drop the member and every file parked for them")
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 1 {
			return fmt.Errorf("%w: remove takes <member> (see catbox status)", errUsage)
		}

		// Both forms destroy parked files irrecoverably, so both
		// carry the same friction as reset.
		if !*yes {
			return fmt.Errorf("%w: remove drops the member and every file parked for them; rerun with --yes", errUsage)
		}

		// The stash host is break-glass: it removes any member.
		if *data != "" || os.Getenv("CATBOX_DATA") != "" {
			dataDir, err := resolveDataDir(*data)
			if err != nil {
				return err
			}

			if err := removeMemberLocal(ctx, dataDir, fs.Arg(0), log); err != nil {
				return err
			}

			fmt.Printf("removed %s\n", fs.Arg(0))

			return nil
		}

		// Over the wire: an admin removes any member, anyone
		// removes themselves.
		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		release, err := lockPeer(ctx)
		if err != nil {
			return err
		}

		defer release()

		conn, cl, err := clientConn(ctx, id)
		if err != nil {
			return err
		}

		defer func() { _ = cl.Close() }()
		defer func() { _ = conn.Close() }()

		if err := clientRemove(conn, fs.Arg(0)); err != nil {
			return err
		}

		fmt.Printf("removed %s\n", fs.Arg(0))

		return nil
	case "admin":
		// Runs on the stash host, like remove --data: the
		// operator grants the first admin (or rescues an
		// admin-less mesh). Admins mint invites and remove
		// members over the wire.
		fs := flag.NewFlagSet("admin", flag.ExitOnError)
		data := fs.String("data", "", "stash data dir (defaults to CATBOX_DATA, else the host default)")
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 1 {
			return fmt.Errorf("%w: admin takes <member>", errUsage)
		}

		dataDir, err := resolveDataDir(*data)
		if err != nil {
			return err
		}

		changed, err := grantAdmin(ctx, dataDir, fs.Arg(0))
		if err != nil {
			return err
		}

		if changed {
			fmt.Printf("%s is now an admin\n", fs.Arg(0))
		} else {
			fmt.Printf("%s is already an admin\n", fs.Arg(0))
		}

		return nil
	case "reset":
		fs := flag.NewFlagSet("reset", flag.ExitOnError)
		yes := fs.Bool("yes", false, "confirm: leave the mesh and wipe the identity")
		_ = fs.Parse(os.Args[2:])

		if !*yes {
			return fmt.Errorf("%w: reset leaves the mesh and wipes this machine's identity; rerun with --yes", errUsage)
		}

		// Leave the roster before wiping the identity, so no ghost
		// member survives. Best effort: offline, the reset still
		// completes and the member can be removed later.
		if id, _, err := loadPeerID("", ""); err == nil {
			release, err := lockPeer(ctx)
			if err != nil {
				return err
			}

			if conn, cl, cerr := clientConn(ctx, id); cerr == nil {
				// The dial key binds the removal to this device:
				// reset can never remove another member.
				if rerr := clientRemove(conn, ""); rerr != nil {
					fmt.Fprintf(os.Stderr, "catbox: could not leave the roster: %v; run \"catbox remove --data /data %s\" on the stash later\n", rerr, id.Name)
				}

				_ = cl.Close()
				_ = conn.Close()
			} else {
				fmt.Fprintf(os.Stderr, "catbox: could not reach the stash: %v; run \"catbox remove --data /data %s\" on the stash later\n", cerr, id.Name)
			}

			release()
		}

		if err := resetPeer(); err != nil {
			return err
		}

		fmt.Println("reset")

		return nil
	case "version":
		fmt.Println("catbox " + version + ", built " + buildStamp())

		return nil
	default:
		fmt.Fprintf(os.Stderr, "catbox: unknown command %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}

	return nil
}

// runJoin registers this machine with the stash, creating the peer
// identity on first use. A join that fails after creating the
// identity rolls it back: a half-joined device would be locked out of
// ever joining again.
func runJoin(ctx context.Context, name, addr, code string) (created bool, err error) {
	id, created, err := loadPeerID(name, tailcat.Addr(addr))
	if err != nil {
		return false, err
	}

	registered := false

	defer func() {
		if created && !registered {
			_ = resetPeer()
		}
	}()

	release, err := lockPeer(ctx)
	if err != nil {
		return created, err
	}

	defer release()

	conn, cl, err := clientConn(ctx, id)
	if err != nil {
		return created, err
	}

	defer func() { _ = cl.Close() }()
	defer func() { _ = conn.Close() }()

	if err := joinReq(conn, id, "", code); err != nil {
		return created, err
	}

	registered = true

	return created, nil
}

// runStatus asks the stash for the roster and this peer's pending
// items — the status command's body, extracted so its every failure
// rides the --json error envelope.
func runStatus(ctx context.Context, jsonOut bool) error {
	id, _, err := loadPeerID("", "")
	if err != nil {
		return err
	}

	release, err := lockPeer(ctx)
	if err != nil {
		return err
	}

	defer release()

	conn, cl, err := clientConn(ctx, id)
	if err != nil {
		return err
	}

	defer func() { _ = cl.Close() }()
	defer func() { _ = conn.Close() }()

	if jsonOut {
		return clientStatusJSON(conn, os.Stdout, id)
	}

	return clientStatus(conn, os.Stdout, id)
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `catbox: async file transfer over tailcat, one stash, daemonless peers

usage:
  catbox <command> [flags]

stash commands:
  serve     run the always-on stash; first boot creates its identity
            [--data DIR] [--region N] [--max 100G] [--ttl 720h] [--health :8081]
  addr      print the stash's tailcat address from its data dir

membership commands:
  join      register this machine under a name
            <name> <invite-token> | <name> <stash-addr> (fresh mesh);
            "-" reads the token from stdin
  invite    print a one-time invite token (admin)
  rename    change this member's name
            <new-name>
  remove    drop a member from the roster (admins over the wire,
            anyone on the stash)
            <member> --yes | --data DIR <member> --yes
  admin     grant a member admin (runs on the stash)
            --data DIR <member>
  reset     leave the mesh (best effort) and wipe this machine's identity
            --yes
  status    who's in the mesh and what's waiting for you
            [--json]

transfer commands:
  send      seal and ship files to a member
            <member> [--text <string>] <file|-|...>
  recv      pull held files into the inbox dir
            [--dir DIR] [<id>...]
  listen    receive direct sends (Ctrl-C to stop)
            [--dir DIR]
  dismiss   refuse delivery of one held item
            <id | name>

other commands:
  version   print the build version and time
  help      print this usage
`)
}

// parseSize parses "100", "10K", "100G", "1.5T" into bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)

	i := len(s)
	for j, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			i = j
			break
		}
	}

	num, unit := s[:i], strings.ToUpper(strings.TrimSpace(s[i:]))

	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}

	mult := int64(1)

	switch unit {
	case "", "B":
	case "K", "KB":
		mult = 1024
	case "M", "MB":
		mult = 1024 * 1024
	case "G", "GB":
		mult = 1024 * 1024 * 1024
	case "T", "TB":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("bad unit in %q", s)
	}

	return int64(f * float64(mult)), nil
}
