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

// sendSources splits the send command line: the target member, then one
// sendSrc per file — "-" is stdin. With text set, the trailing args are the
// message body instead.
func sendSources(text bool, files []string) []sendSrc {
	if text {
		return []sendSrc{{body: []byte(strings.Join(files, " "))}}
	}

	srcs := make([]sendSrc, len(files))

	for i, f := range files {
		if f == "-" {
			srcs[i] = sendSrc{stdin: true}
		} else {
			srcs[i] = sendSrc{path: f}
		}
	}

	return srcs
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
