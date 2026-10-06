package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tailscale/tailcat"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "catbox:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

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

		id, created, err := loadPeerID(*name, tailcat.Addr(fs.Arg(0)))
		if err != nil {
			return err
		}

		conn, cl, err := clientConn(ctx, id)
		if err != nil {
			return err
		}
		defer func() { _ = cl.Close() }()
		defer func() { _ = conn.Close() }()

		if err := joinReq(conn, id, ""); err != nil {
			return err
		}

		if created {
			fmt.Printf("joined as %s\n", id.Name)
		} else {
			fmt.Printf("already joined as %s (roster refreshed)\n", id.Name)
		}

		return nil
	case "recv":
		fs := flag.NewFlagSet("recv", flag.ExitOnError)
		dir := fs.String("dir", "", "inbox dir (default: OS downloads + /catbox)")
		stay := fs.Bool("stay", false, "after pulling, stay online for direct sends (Ctrl-C to stop)")
		_ = fs.Parse(os.Args[2:])

		if *dir == "" {
			var err error

			*dir, err = downloadDir()
			if err != nil {
				return err
			}
		}

		return runRecv(ctx, log, *dir, *stay)
	case "invite":
		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		fmt.Printf("catbox join --name <name> '%s'\n", id.StorerAddr)

		return nil
	case "status":
		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		conn, cl, err := clientConn(ctx, id)
		if err != nil {
			return err
		}

		defer func() { _ = cl.Close() }()
		defer func() { _ = conn.Close() }()

		return clientStatus(conn, os.Stdout, id)
	case "send":
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])

		if fs.NArg() != 2 {
			return errors.New("send takes <member> <file>")
		}

		id, _, err := loadPeerID("", "")
		if err != nil {
			return err
		}

		conn, cl, err := clientConn(ctx, id)
		if err != nil {
			return err
		}

		defer func() { _ = cl.Close() }()
		defer func() { _ = conn.Close() }()

		return clientSend(ctx, conn, id, fs.Arg(0), fs.Arg(1))
	default:
		usage()
		os.Exit(1)
	}

	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `catbox: async file transfer over tailcat, one storer, daemonless peers

usage:
  catbox serve  [--data DIR] [--region N] [--max 100G] [--ttl 720h] [--health :8081]
  catbox addr
  catbox join   --name NAME <storer-addr>
  catbox invite
  catbox send   <member> <file>
  catbox recv   [--dir DIR] [--stay]
  catbox status
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
