package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// target member. With text set, the remaining args are the message;
// otherwise "-" is stdin and any other arg a file.
func sendSources(text bool, args []string) (string, []sendSrc, error) {
	const usage = "%w: send takes <member> <file|-|...> or <member> --text <string>"

	if len(args) < 1 {
		return "", nil, fmt.Errorf(usage, errUsage)
	}

	member := args[0]
	rest := args[1:]

	var srcs []sendSrc

	if text {
		if len(rest) == 0 {
			return "", nil, fmt.Errorf(usage, errUsage)
		}

		srcs = append(srcs, sendSrc{body: []byte(strings.Join(rest, " "))})
	} else {
		for _, a := range rest {
			if a == "-" {
				srcs = append(srcs, sendSrc{stdin: true})
			} else {
				srcs = append(srcs, sendSrc{path: a})
			}
		}
	}

	if len(srcs) == 0 {
		return "", nil, fmt.Errorf(usage, errUsage)
	}

	return member, srcs, nil
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
