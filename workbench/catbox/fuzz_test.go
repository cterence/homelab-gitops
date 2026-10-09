package main

// Fuzz targets for the parse surfaces at the trust boundaries: the
// wire (readMsg) and the sealed stream (openStream). The seed corpus
// runs under plain `go test`; `go test -fuzz=FuzzX` explores further.

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"

	"tailscale.com/types/key"
)

// FuzzReadMsg: arbitrary bytes on the wire must never panic, and a
// message that decodes must survive a re-encode/re-read cycle.
func FuzzReadMsg(f *testing.F) {
	seeds := []msg{
		{Op: opJoin, Name: "emu"},
		{Op: opSend, Target: "emu", FileName: "hello.txt", Size: 25, SHA: "ab"},
		{Op: opFetch, ID: "f48e17ff", Have: 2 * chunkSize},
		{Op: opItems, Items: []item{{ID: "x", FileName: "f", SHA: "s", From: "emu", Plain: 1, Size: 2}}},
	}
	for _, m := range seeds {
		var b bytes.Buffer
		if err := writeMsg(&b, m); err != nil {
			f.Fatal(err)
		}

		f.Add(b.Bytes())
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := readMsg(bytes.NewReader(b))
		if err != nil {
			return
		}

		var out bytes.Buffer
		if err := writeMsg(&out, m); err != nil {
			return
		}

		m2, err := readMsg(&out)
		if err != nil {
			t.Fatalf("re-read of decoded message: %v", err)
		}

		if m.Op != m2.Op || m.Size != m2.Size || m.Have != m2.Have || m.OK != m2.OK {
			t.Fatal("message not stable across write-read")
		}
	})
}

// FuzzSealOpenRoundTrip: sealing arbitrary plaintext, opened fresh
// and resumed at a chunk boundary, must reproduce the input whole.
func FuzzSealOpenRoundTrip(f *testing.F) {
	f.Add([]byte("hello"), uint16(0))
	f.Add([]byte{}, uint16(0))
	f.Add(make([]byte, chunkSize*3+123), uint16(2))

	f.Fuzz(func(t *testing.T, plain []byte, k uint16) {
		sender := key.NewNode()
		recipient := key.NewNode()
		shaHex := shaHexOf(plain)

		var sealed bytes.Buffer
		if _, err := sealStream(sender, recipient.Public(), &sealed, bytes.NewReader(plain), shaHex, 0); err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer

		gotSender, size, sha, err := openStream(recipient, bytes.NewReader(sealed.Bytes()), &out, 0, nil)
		if err != nil {
			t.Fatal(err)
		}

		if gotSender != sender.Public() || size != int64(len(plain)) || sha != shaHex || !bytes.Equal(out.Bytes(), plain) {
			t.Fatal("fresh open mismatch")
		}

		// Resume: the sender re-seals from a chunk boundary, the
		// receiver re-hashes its kept prefix and continues.
		off := int64(k) % (int64(len(plain))/chunkSize + 1) * chunkSize
		if off <= 0 {
			return
		}

		var resumed bytes.Buffer

		rr := bytes.NewReader(plain)
		if _, err := rr.Seek(off, io.SeekStart); err != nil {
			t.Fatal(err)
		}

		if _, err := sealStream(sender, recipient.Public(), &resumed, rr, shaHex, off); err != nil {
			t.Fatal(err)
		}

		h := sha256.New()
		h.Write(plain[:off])

		var tail bytes.Buffer

		_, size, sha, err = openStream(recipient, bytes.NewReader(resumed.Bytes()), &tail, off, h)
		if err != nil {
			t.Fatal(err)
		}

		want := append(append([]byte{}, plain[:off]...), tail.Bytes()...)
		if size != int64(len(plain)) || sha != shaHex || !bytes.Equal(want, plain) {
			t.Fatal("resumed open mismatch")
		}
	})
}

// FuzzOpenStreamGarbage: corrupt or hostile sealed streams must
// error out, never panic.
func FuzzOpenStreamGarbage(f *testing.F) {
	f.Add([]byte("garbage"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, b []byte) {
		recipient := key.NewNode()

		var out bytes.Buffer

		_, _, _, _ = openStream(recipient, bytes.NewReader(b), &out, 0, nil)
	})
}
