package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// sendSrc is one transfer of a send command: a file on disk, the
// --text body, or stdin ("-" in place of a file).
type sendSrc struct {
	path  string
	body  []byte
	stdin bool
}

// sendSources splits a send command line: the first arg is the
// target member, "-" is stdin, any other arg a file, and a non-empty
// --text appends one text transfer after them.
func sendSources(text string, args []string) (string, []sendSrc, error) {
	const usage = "%w: send takes <member> [--text <string>] <file|-|...>"

	if len(args) < 1 {
		return "", nil, fmt.Errorf(usage, errUsage)
	}

	member := args[0]

	var srcs []sendSrc

	for _, a := range args[1:] {
		if a == "-" {
			srcs = append(srcs, sendSrc{stdin: true})
		} else {
			srcs = append(srcs, sendSrc{path: a})
		}
	}

	if text != "" {
		srcs = append(srcs, sendSrc{body: []byte(text)})
	}

	if len(srcs) == 0 {
		return "", nil, fmt.Errorf(usage, errUsage)
	}

	return member, srcs, nil
}

// parseSendFlags extracts flags from anywhere in args — flag.Parse
// stops at the first non-flag argument — returning the positionals.
func parseSendFlags(fs *flag.FlagSet, args []string) []string {
	var positional []string

	for {
		_ = fs.Parse(args)

		rest := fs.Args()
		if len(rest) == 0 {
			return positional
		}

		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// textFileName is the delivered name for --text and stdin sends.
func textFileName(t time.Time) string {
	return "text-" + t.Format("20060102-150405") + ".txt"
}

// spoolSend stages body in a temp dir under the delivered name, so
// clientSend ships it like any file. Remove the dir after the send.
func spoolSend(body []byte) (string, error) {
	dir, err := os.MkdirTemp("", "catbox-send-*")
	if err != nil {
		return "", fmt.Errorf("creating spool dir: %w", err)
	}

	path := filepath.Join(dir, textFileName(time.Now()))

	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", fmt.Errorf("writing spool: %w", err)
	}

	return path, nil
}
