package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSendSources(t *testing.T) {
	tests := []struct {
		name string
		text bool
		args []string
		want []sendSrc
	}{
		{"one file", false, []string{"a.txt"}, []sendSrc{{path: "a.txt"}}},
		{
			"files mixed with stdin",
			false,
			[]string{"a.txt", "-", "b.txt"},
			[]sendSrc{{path: "a.txt"}, {stdin: true}, {path: "b.txt"}},
		},
		{"stdin only", false, []string{"-"}, []sendSrc{{stdin: true}}},
		{"text only", true, []string{"hi"}, []sendSrc{{body: []byte("hi")}}},
		{
			"multi-word text joined",
			true,
			[]string{"hi", "there"},
			[]sendSrc{{body: []byte("hi there")}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srcs := sendSources(tt.text, tt.args)

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
