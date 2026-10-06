package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strconv"
	"testing"

	"tailscale.com/types/key"
)

func TestSealOpenRoundTrip(t *testing.T) {
	sender := key.NewNode()
	recipient := key.NewNode()

	sizes := []int{0, 1, 63, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 123}
	for _, size := range sizes {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			plain := make([]byte, size)
			rand.Read(plain)

			var sealed bytes.Buffer

			gotSize, gotSHA, err := sealStream(sender, recipient.Public(), &sealed, bytes.NewReader(plain))
			if err != nil {
				t.Fatal(err)
			}

			if gotSize != int64(size) {
				t.Fatalf("plain size = %d, want %d", gotSize, size)
			}

			var out bytes.Buffer

			gotSender, openSize, openSHA, err := openStream(recipient, &sealed, &out)
			if err != nil {
				t.Fatal(err)
			}

			if gotSender != sender.Public() {
				t.Fatalf("sender = %v, want %v", gotSender, sender.Public())
			}

			if openSize != int64(size) {
				t.Fatalf("opened size = %d, want %d", openSize, size)
			}

			if gotSHA != openSHA {
				t.Fatalf("sha mismatch: sealed %s opened %s", gotSHA, openSHA)
			}

			if !bytes.Equal(out.Bytes(), plain) {
				t.Fatal("content mismatch")
			}
		})
	}
}

func TestOpenStreamRejects(t *testing.T) {
	sender := key.NewNode()
	recipient := key.NewNode()

	seed := func() []byte {
		var sealed bytes.Buffer
		if _, _, err := sealStream(sender, recipient.Public(), &sealed, bytes.NewReader([]byte("hello catbox"))); err != nil {
			t.Fatal(err)
		}

		return sealed.Bytes()
	}

	t.Run("wrong recipient", func(t *testing.T) {
		other := key.NewNode()

		var out bytes.Buffer
		if _, _, _, err := openStream(other, bytes.NewReader(seed()), &out); !errors.Is(err, errBadHeader) {
			t.Fatalf("err = %v, want errBadHeader", err)
		}
	})

	t.Run("corrupt chunk", func(t *testing.T) {
		b := seed()
		b[len(b)-6] ^= 0xff // inside the last ciphertext

		var out bytes.Buffer
		if _, _, _, err := openStream(recipient, bytes.NewReader(b), &out); !errors.Is(err, errCorrupt) {
			t.Fatalf("err = %v, want errCorrupt", err)
		}
	})

	t.Run("truncated stream", func(t *testing.T) {
		b := seed()
		b = b[:len(b)-5] // cut before the terminator

		var out bytes.Buffer
		if _, _, _, err := openStream(recipient, bytes.NewReader(b), &out); !errors.Is(err, errCorrupt) {
			t.Fatalf("err = %v, want errCorrupt", err)
		}
	})

	t.Run("truncated header", func(t *testing.T) {
		var out bytes.Buffer
		if _, _, _, err := openStream(recipient, bytes.NewReader([]byte("short")), &out); !errors.Is(err, errCorrupt) {
			t.Fatalf("err = %v, want errCorrupt", err)
		}
	})
}
