package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"testing"

	"tailscale.com/types/key"
)

// shaHexOf hashes the plaintext the way a sending command would
// before sealing: the SHA keys the deterministic file secret.
func shaHexOf(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

func TestSealOpenRoundTrip(t *testing.T) {
	sender := key.NewNode()
	recipient := key.NewNode()

	sizes := []int{0, 1, 63, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 123}
	for _, size := range sizes {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			plain := make([]byte, size)
			rand.Read(plain)
			shaHex := shaHexOf(plain)

			var sealed bytes.Buffer

			gotSize, err := sealStream(sender, recipient.Public(), &sealed, bytes.NewReader(plain), shaHex, 0)
			if err != nil {
				t.Fatal(err)
			}

			if gotSize != int64(size) {
				t.Fatalf("plain size = %d, want %d", gotSize, size)
			}

			var out bytes.Buffer

			gotSender, openSize, openSHA, err := openStream(recipient, &sealed, &out, 0, nil)
			if err != nil {
				t.Fatal(err)
			}

			if gotSender != sender.Public() {
				t.Fatalf("sender = %v, want %v", gotSender, sender.Public())
			}

			if openSize != int64(size) {
				t.Fatalf("opened size = %d, want %d", openSize, size)
			}

			if shaHex != openSHA {
				t.Fatalf("sha mismatch: sealed %s opened %s", shaHex, openSHA)
			}

			if !bytes.Equal(out.Bytes(), plain) {
				t.Fatal("content mismatch")
			}
		})
	}
}

// TestSealDeterministic pins the resume invariant: the same
// (sender, recipient, file) always seals to the same ciphertext —
// the header's sealed box re-randomizes per call, but the chunks a
// resumed attempt re-sends must decrypt against the receiver's partial.
func TestSealDeterministic(t *testing.T) {
	sender := key.NewNode()
	recipient := key.NewNode()

	plain := make([]byte, 2*chunkSize+77)
	rand.Read(plain)
	shaHex := shaHexOf(plain)

	var a, b bytes.Buffer
	if _, err := sealStream(sender, recipient.Public(), &a, bytes.NewReader(plain), shaHex, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := sealStream(sender, recipient.Public(), &b, bytes.NewReader(plain), shaHex, 0); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(a.Bytes()[headerLen:], b.Bytes()[headerLen:]) {
		t.Fatal("same file sealed twice differs: resume would break")
	}
}

// TestSealResumeRoundTrip simulates a cut transfer: the receiver keeps
// the first k chunks, the sender re-seals from that boundary, and the
// resumed open must produce the whole file with the whole-file SHA.
func TestSealResumeRoundTrip(t *testing.T) {
	sender := key.NewNode()
	recipient := key.NewNode()

	plain := make([]byte, 3*chunkSize+123)
	rand.Read(plain)
	shaHex := shaHexOf(plain)

	// First attempt: full seal, then the receiver cuts after k chunks
	// (the missing terminator is the cut itself).
	var sealed bytes.Buffer
	if _, err := sealStream(sender, recipient.Public(), &sealed, bytes.NewReader(plain), shaHex, 0); err != nil {
		t.Fatal(err)
	}

	k := int64(2)

	var partialBuf bytes.Buffer
	if _, _, _, err := openStream(recipient, io.LimitReader(bytes.NewReader(sealed.Bytes()), headerLen+k*chunkFrameLen), &partialBuf, 0, nil); err != nil {
		if !errors.Is(err, errCorrupt) {
			t.Fatalf("cut open: %v", err)
		}
	}

	if int64(partialBuf.Len()) != k*chunkSize {
		t.Fatalf("partial = %d bytes, want %d", partialBuf.Len(), k*chunkSize)
	}

	// Second attempt: the sender resumes from the receiver's boundary.
	// sealStream takes the reader already positioned at the offset.
	var resumed bytes.Buffer

	rr := bytes.NewReader(plain)
	if _, err := rr.Seek(k*chunkSize, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	if _, err := sealStream(sender, recipient.Public(), &resumed, rr, shaHex, k*chunkSize); err != nil {
		t.Fatal(err)
	}

	// The receiver re-hashes its kept prefix and continues.
	h := sha256.New()
	h.Write(partialBuf.Bytes())

	var out bytes.Buffer

	gotSender, openSize, openSHA, err := openStream(recipient, bytes.NewReader(resumed.Bytes()), &out, k*chunkSize, h)
	if err != nil {
		t.Fatal(err)
	}

	_ = gotSender

	if openSize != int64(len(plain)) {
		t.Fatalf("resumed size = %d, want %d", openSize, len(plain))
	}

	if openSHA != shaHex {
		t.Fatalf("resumed sha = %s, want %s", openSHA, shaHex)
	}

	want := append(append([]byte{}, partialBuf.Bytes()...), out.Bytes()...)
	if !bytes.Equal(want, plain) {
		t.Fatal("resumed content mismatch")
	}
}

func TestOpenStreamRejects(t *testing.T) {
	sender := key.NewNode()
	recipient := key.NewNode()

	seed := func() []byte {
		plain := []byte("hello catbox")

		var sealed bytes.Buffer
		if _, err := sealStream(sender, recipient.Public(), &sealed, bytes.NewReader(plain), shaHexOf(plain), 0); err != nil {
			t.Fatal(err)
		}

		return sealed.Bytes()
	}

	t.Run("wrong recipient", func(t *testing.T) {
		other := key.NewNode()

		var out bytes.Buffer
		if _, _, _, err := openStream(other, bytes.NewReader(seed()), &out, 0, nil); !errors.Is(err, errBadHeader) {
			t.Fatalf("err = %v, want errBadHeader", err)
		}
	})

	t.Run("corrupt chunk", func(t *testing.T) {
		b := seed()
		b[len(b)-6] ^= 0xff // inside the last ciphertext

		var out bytes.Buffer
		if _, _, _, err := openStream(recipient, bytes.NewReader(b), &out, 0, nil); !errors.Is(err, errCorrupt) {
			t.Fatalf("err = %v, want errCorrupt", err)
		}
	})

	t.Run("truncated stream", func(t *testing.T) {
		b := seed()
		b = b[:len(b)-5] // cut before the terminator

		var out bytes.Buffer
		if _, _, _, err := openStream(recipient, bytes.NewReader(b), &out, 0, nil); !errors.Is(err, errCorrupt) {
			t.Fatalf("err = %v, want errCorrupt", err)
		}
	})

	t.Run("truncated header", func(t *testing.T) {
		var out bytes.Buffer
		if _, _, _, err := openStream(recipient, bytes.NewReader([]byte("short")), &out, 0, nil); !errors.Is(err, errCorrupt) {
			t.Fatalf("err = %v, want errCorrupt", err)
		}
	})
}
