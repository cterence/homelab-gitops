package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "catbox:", err)
		os.Exit(1)
	}
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

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		data := fs.String("data", envOr("CATBOX_DATA", "."), "data dir for identity, roster, and spool")
		region := fs.Int64("region", 0, "DERP region ID; 0 probes once on first boot")
		max := fs.String("max", "100G", "spool capacity cap")
		ttl := fs.Duration("ttl", 30*24*time.Hour, "spool retention")
		health := fs.String("health", ":8081", "health listen address")
		_ = fs.Parse(os.Args[2:])

		maxN, err := parseSize(*max)
		if err != nil {
			return err
		}

		return runServe(ctx, log, *data, *region, maxN, *ttl, *health)
	case "addr":
		// Reads the same dir serve uses: CATBOX_DATA env, else ".".
		id, _, err := loadStorerIdentity(ctx, envOr("CATBOX_DATA", "."), -1)
		if err != nil {
			return err
		}

		fmt.Println(id.Public.Addr())

		return nil
	case "join":
		fs := flag.NewFlagSet("join", flag.ExitOnError)
		name := fs.String("name", "", "this machine's member name")
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 1 {
			return errors.New("join takes one storer address argument")
		}

		created, err := runJoin(ctx, *name, fs.Arg(0))
		if err != nil {
			return err
		}

		if created {
			fmt.Printf("joined as %s\n", *name)
		} else {
			fmt.Printf("already joined as %s (roster refreshed)\n", *name)
		}

		return nil
	case "rename":
		fs := flag.NewFlagSet("rename", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 1 {
			return errors.New("rename takes <new-name>")
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

		// The rename rides the join op: the storer validates the name
		// and retargets parked files. Stop recv --listen first, or its
		// registered listener address goes stale.
		id.Name = fs.Arg(0)

		if err := joinReq(conn, id, ""); err != nil {
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
		listen := fs.Bool("listen", false, "listen for direct sends only, no storer pull (Ctrl-C to stop)")
		_ = fs.Parse(os.Args[2:])

		if *dir == "" {
			var err error

			*dir, err = downloadDir()
			if err != nil {
				return err
			}
		}

		return runRecv(ctx, log, *dir, *listen, fs.Args())
	case "invite":
		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		fmt.Printf("catbox join --name <name> '%s'\n", id.StorerAddr)

		return nil
	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		jsonOut := fs.Bool("json", false, "machine-readable status for UIs")
		_ = fs.Parse(os.Args[2:])

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

		if *jsonOut {
			return clientStatusJSON(conn, os.Stdout, id)
		}

		return clientStatus(conn, os.Stdout, id)
	case "send":
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() < 2 {
			return errors.New("send takes <member> <file> [<file>...]")
		}

		for _, path := range fs.Args()[1:] {
			if fi, err := os.Stat(path); err != nil {
				return err
			} else if fi.IsDir() {
				return errors.New("send takes files, " + path + " is a directory")
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

		for _, path := range fs.Args()[1:] {
			// One storer conn per file: a long transfer starves the
			// storer's idle deadline, and the next file must not
			// inherit a dead conn. The SHA is known before sealing:
			// it keys the deterministic file secret and the
			// receiver's resume partial.
			shaHex, err := fileSHA256(path)
			if err != nil {
				return fmt.Errorf("hashing %s: %w", path, err)
			}

			conn, err := storerConn(ctx, id)
			if err != nil {
				return err
			}

			err = clientSend(ctx, conn, id, fs.Arg(0), path, shaHex)
			_ = conn.Close()

			if err != nil {
				return fmt.Errorf("sending %s: %w", path, err)
			}
		}

		return nil
	case "dismiss":
		fs := flag.NewFlagSet("dismiss", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 1 {
			return errors.New("dismiss takes <id> (see catbox status)")
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

		if err := clientDismiss(conn, fs.Arg(0)); err != nil {
			return err
		}

		fmt.Printf("dismissed %s\n", fs.Arg(0))

		return nil
	case "remove":
		fs := flag.NewFlagSet("remove", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 1 {
			return errors.New("remove takes <member> (see catbox status)")
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

		if err := clientRemove(conn, fs.Arg(0)); err != nil {
			return err
		}

		fmt.Printf("removed %s\n", fs.Arg(0))

		return nil
	case "reset":
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
					fmt.Fprintf(os.Stderr, "catbox: could not leave the roster: %v; run \"catbox remove %s\" from a member later\n", rerr, id.Name)
				}

				_ = cl.Close()
				_ = conn.Close()
			} else {
				fmt.Fprintf(os.Stderr, "catbox: could not reach the storer: %v; run \"catbox remove %s\" from a member later\n", cerr, id.Name)
			}

			release()
		}

		if err := resetPeer(); err != nil {
			return err
		}

		fmt.Println("reset")

		return nil
	case "version":
		fmt.Println("catbox " + version)

		return nil
	default:
		usage()
		os.Exit(1)
	}

	return nil
}

// runJoin registers this machine with the storer, creating the peer
// identity on first use. A join that fails after creating the
// identity rolls it back: a half-joined device would be locked out of
// ever joining again.
func runJoin(ctx context.Context, name, addr string) (created bool, err error) {
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

	if err := joinReq(conn, id, ""); err != nil {
		return created, err
	}

	registered = true

	return created, nil
}

func usage() {
	fmt.Fprint(os.Stderr, `catbox: async file transfer over tailcat, one storer, daemonless peers

usage:
  catbox <command> [flags]

storer commands:
  serve     run the always-on storer; first boot creates its identity
            [--data DIR] [--region N] [--max 100G] [--ttl 720h] [--health :8081]
  addr      print the storer's tailcat address from its data dir

membership commands:
  join      register this machine under a name
            --name NAME <storer-addr>
  invite    print the join line for enrolling another peer
  rename    change this member's name
            <new-name>
  remove    drop a member from the roster; their parked items go too
            <member>
  reset     leave the mesh (best effort) and wipe this machine's identity
  status    who's in the mesh and what's waiting for you
            [--json]

transfer commands:
  send      seal and ship files to a member
            <member> <file> [<file>...]
  recv      pull held files, or listen for direct sends
            [--dir DIR] [--listen] [<id>...]
  dismiss   refuse delivery of one held item
            <id>

other commands:
  version   print the build version
`)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
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
