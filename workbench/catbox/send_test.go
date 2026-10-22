package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSendSources(t *testing.T) {
	tests := []struct {
		name       string
		text       bool
		args       []string
		wantMember string
		want       []sendSrc
		wantErr    bool
	}{
		{"no args", false, nil, "", nil, true},
		{"member without sources", false, []string{"bob"}, "", nil, true},
		{"one file", false, []string{"bob", "a.txt"}, "bob", []sendSrc{{path: "a.txt"}}, false},
		{
			"files mixed with stdin",
			false,
			[]string{"bob", "a.txt", "-", "b.txt"},
			"bob",
			[]sendSrc{{path: "a.txt"}, {stdin: true}, {path: "b.txt"}},
			false,
		},
		{"stdin only", false, []string{"bob", "-"}, "bob", []sendSrc{{stdin: true}}, false},
		{"text flag without body", true, []string{"bob"}, "", nil, true},
		{"text only", true, []string{"bob", "hi"}, "bob", []sendSrc{{body: []byte("hi")}}, false},
		{
			"multi-word text joined",
			true,
			[]string{"bob", "hi", "there"},
			"bob",
			[]sendSrc{{body: []byte("hi there")}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			member, srcs, err := sendSources(tt.text, tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("sendSources() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				if !errors.Is(err, errUsage) {
					t.Fatalf("sendSources() error = %v, want errUsage", err)
				}

				return
			}

			if member != tt.wantMember {
				t.Fatalf("member = %q, want %q", member, tt.wantMember)
			}

			if len(srcs) != len(tt.want) {
				t.Fatalf("srcs = %+v, want %+v", srcs, tt.want)
			}

			for i := range srcs {
				got, want := srcs[i], tt.want[i]
				if got.path != want.path || got.stdin != want.stdin || !bytes.Equal(got.body, want.body) {
					t.Fatalf("srcs[%d] = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestParseSendFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantText bool
		wantRest []string
	}{
		{"flag before positionals", []string{"--text", "bob", "hi there"}, true, []string{"bob", "hi there"}},
		{"flag between positionals", []string{"bob", "--text", "hi there"}, true, []string{"bob", "hi there"}},
		{"flag after positionals", []string{"bob", "hi there", "--text"}, true, []string{"bob", "hi there"}},
		{"no flags", []string{"bob", "a.txt"}, false, []string{"bob", "a.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("send", flag.ExitOnError)
			text := fs.Bool("text", false, "")

			rest := parseSendFlags(fs, tt.args)

			if *text != tt.wantText {
				t.Fatalf("text = %v, want %v", *text, tt.wantText)
			}

			if strings.Join(rest, " ") != strings.Join(tt.wantRest, " ") {
				t.Fatalf("rest = %q, want %q", rest, tt.wantRest)
			}
		})
	}
}

func TestTextFileName(t *testing.T) {
	got := textFileName(time.Date(2026, 10, 17, 9, 5, 3, 0, time.UTC))
	if got != "text-20261017-090503.txt" {
		t.Fatalf("textFileName() = %q, want %q", got, "text-20261017-090503.txt")
	}
}

func TestSpoolSend(t *testing.T) {
	path, err := spoolSend([]byte("hello"))
	if err != nil {
		t.Fatalf("spoolSend() error = %v", err)
	}

	defer func() { _ = os.RemoveAll(filepath.Dir(path)) }()

	if base := filepath.Base(path); !strings.HasPrefix(base, "text-") || !strings.HasSuffix(base, ".txt") {
		t.Fatalf("spooled name = %q, want text-*.txt", base)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading spool: %v", err)
	}

	if string(b) != "hello" {
		t.Fatalf("spooled body = %q, want %q", b, "hello")
	}
}
