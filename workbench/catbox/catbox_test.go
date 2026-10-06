package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tailscale.com/types/key"
)

func TestWriteReadMsg(t *testing.T) {
	var buf bytes.Buffer

	in := msg{Op: opSend, Target: "nas", FileName: "f.txt", Size: 42, Members: []member{{Name: "nas"}}}
	if err := writeMsg(&buf, in); err != nil {
		t.Fatal(err)
	}

	out, err := readMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if out.Op != opSend || out.Target != "nas" || out.FileName != "f.txt" || out.Size != 42 {
		t.Fatalf("out = %+v", out)
	}

	if len(out.Members) != 1 || out.Members[0].Name != "nas" {
		t.Fatalf("members = %+v", out.Members)
	}
}

func TestReadMsgRejects(t *testing.T) {
	t.Run("oversize frame", func(t *testing.T) {
		var buf bytes.Buffer

		_ = binary.Write(&buf, binary.BigEndian, uint32(msgMax+1))

		if _, err := readMsg(&buf); err == nil {
			t.Fatal("oversize frame accepted")
		}
	})
	t.Run("bad json", func(t *testing.T) {
		var buf bytes.Buffer

		_ = binary.Write(&buf, binary.BigEndian, uint32(3))
		buf.WriteString("{")

		if _, err := readMsg(&buf); err == nil {
			t.Fatal("bad json accepted")
		}
	})
	t.Run("eof", func(t *testing.T) {
		if _, err := readMsg(bytes.NewReader(nil)); err == nil {
			t.Fatal("eof accepted")
		}
	})
}

func TestRosterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := rosterPath(dir)
	k1 := key.NewNode().Public()
	k2 := key.NewNode().Public()
	d2 := key.NewNode().Public()

	in := []member{
		{Name: "laptop", Key: k1, DialKey: key.NewNode().Public(), Addr: "tcexample", Joined: 1},
		{Name: "nas", Key: k2, DialKey: d2, Joined: 2},
	}
	if err := saveRoster(path, in); err != nil {
		t.Fatal(err)
	}

	out, err := loadRoster(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(out) != 2 || out[0] != in[0] || out[1] != in[1] {
		t.Fatalf("out = %+v", out)
	}

	if m, ok := memberByName(out, "nas"); !ok || m.Key != k2 {
		t.Fatalf("memberByName = %+v %v", m, ok)
	}

	if _, ok := memberByName(out, "ghost"); ok {
		t.Fatal("ghost found")
	}

	if m, ok := memberByDialKey(out, d2); !ok || m.Name != "nas" {
		t.Fatalf("memberByDialKey = %+v %v", m, ok)
	}

	if _, ok := memberByDialKey(out, k1); ok {
		t.Fatal("identity key must not match a connection")
	}
}

func TestLoadRosterMissing(t *testing.T) {
	out, err := loadRoster(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatalf("missing roster should be empty: %v", err)
	}

	if len(out) != 0 {
		t.Fatalf("out = %+v", out)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		want string
	}{
		{"fresh.txt", "fresh.txt"},
		{"photo.jpg", "photo-2.jpg"},     // photo.jpg and photo-1.jpg occupied
		{"photo-1.jpg", "photo-1-1.jpg"}, // base-N applies to the given name
		{"noext", "noext-1"},             // occupied, no extension
	}

	if err := os.WriteFile(filepath.Join(dir, "photo.jpg"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "photo-1.jpg"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "noext"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uniquePath(dir, tt.name); filepath.Base(got) != tt.want {
				t.Fatalf("uniquePath(%q) = %q, want %q", tt.name, filepath.Base(got), tt.want)
			}
		})
	}
}

func TestValidName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"laptop", true},
		{"nas-2", true},
		{"a", true},
		{"", false},
		{"-x", false},
		{"Laptop", false},
		{"my laptop", false},
		{"this-name-is-way-too-long-for-the-roster", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validName(tt.name); got != tt.want {
				t.Fatalf("validName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		err  bool
	}{
		{"100", 100, false},
		{"10K", 10 * 1024, false},
		{"1.5M", 1024*1024 + 512*1024, false},
		{"100G", 100 * 1024 * 1024 * 1024, false},
		{"1T", 1024 * 1024 * 1024 * 1024, false},
		{"abc", 0, true},
		{"12Z", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseSize(tt.in)
			if tt.err != (err != nil) {
				t.Fatalf("err = %v, want err %v", err, tt.err)
			}

			if !tt.err && got != tt.want {
				t.Fatalf("parseSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestSpoolRoundTrip(t *testing.T) {
	dir := t.TempDir()

	sp, err := openSpool(dir)
	if err != nil {
		t.Fatal(err)
	}

	sender := key.NewNode()
	recipient := key.NewNode()

	var sealed bytes.Buffer
	if _, _, err := sealStream(sender, recipient.Public(), &sealed, bytes.NewReader([]byte("payload"))); err != nil {
		t.Fatal(err)
	}

	wantSize := int64(sealed.Len())

	meta := spoolMeta{FileName: "f.bin", From: "laptop", Target: "nas"}

	put, err := sp.put(&sealed, meta)
	if err != nil {
		t.Fatal(err)
	}

	items, err := sp.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 1 || items[0].ID != put.ID || items[0].Size != wantSize {
		t.Fatalf("items = %+v", items)
	}

	gotMeta, blob, err := sp.open(put.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blob.Close() }()

	if gotMeta.FileName != "f.bin" || gotMeta.From != "laptop" {
		t.Fatalf("meta = %+v", gotMeta)
	}

	var out bytes.Buffer
	if _, err := out.ReadFrom(blob); err != nil {
		t.Fatal(err)
	}

	plain, sha, err := openStream(recipient, &out, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	if plain != 7 || sha == "" {
		t.Fatalf("plain = %d", plain)
	}

	usage, err := sp.usage()
	if err != nil {
		t.Fatal(err)
	}

	if usage < wantSize {
		t.Fatalf("usage = %d, want at least %d", usage, wantSize)
	}

	if err := sp.delete(put.ID); err != nil {
		t.Fatal(err)
	}

	if err := sp.delete(put.ID); err != nil {
		t.Fatalf("double delete should be a no-op: %v", err)
	}

	if items, _ = sp.items(); len(items) != 0 {
		t.Fatalf("spool not empty: %+v", items)
	}
}

func TestSpoolSweep(t *testing.T) {
	dir := t.TempDir()

	sp, err := openSpool(dir)
	if err != nil {
		t.Fatal(err)
	}

	recipient := key.NewNode().Public()

	old := spoolMeta{FileName: "old.bin", At: time.Now().Add(-48 * time.Hour).Unix()}

	fresh := spoolMeta{FileName: "new.bin"}
	for _, m := range []spoolMeta{old, fresh} {
		var sealed bytes.Buffer
		if _, _, err := sealStream(key.NewNode(), recipient, &sealed, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}

		if _, err := sp.put(&sealed, m); err != nil {
			t.Fatal(err)
		}
	}

	if err := sp.sweep(24 * time.Hour); err != nil {
		t.Fatal(err)
	}

	items, err := sp.items()
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 1 || items[0].FileName != "new.bin" {
		t.Fatalf("after sweep: %+v", items)
	}
}
