package main

// Sealed streams: a header (sender key + sealed box over the file key
// and nonce prefix) followed by length-framed XChaCha20-Poly1305
// chunks, age's STREAM construction. A zero-length frame terminates.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

const (
	chunkSize  = 64 << 10
	nonceLen   = chacha20poly1305.NonceSizeX // 24
	prefixLen  = 13
	fileKeyLen = chacha20poly1305.KeySize
	boxOver    = 24 + 16 // sealed box: nonce + Poly1305 tag
	headerLen  = key.NodePublicRawLen + boxOver + fileKeyLen + prefixLen
)

var (
	errBadHeader = errors.New("cannot open sealed header (wrong key or corrupt header)")
	errCorrupt   = errors.New("corrupt or truncated sealed stream")
)

// sealStream seals src to dst for recipient. It returns the plaintext
// size and hex SHA-256 of the whole file.
func sealStream(sender key.NodePrivate, recipient key.NodePublic, dst io.Writer, src io.Reader) (int64, string, error) {
	var secret [fileKeyLen + prefixLen]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return 0, "", fmt.Errorf("generating file key: %w", err)
	}

	aead, err := chacha20poly1305.NewX(secret[:fileKeyLen])
	if err != nil {
		return 0, "", err
	}

	senderRaw, err := tailcat.NodePublic{NodePublic: sender.Public()}.MarshalBinary()
	if err != nil {
		return 0, "", fmt.Errorf("encoding sender key: %w", err)
	}

	header := append(senderRaw, sender.SealTo(recipient, secret[:])...)
	if len(header) != headerLen {
		return 0, "", fmt.Errorf("header is %d bytes, want %d", len(header), headerLen)
	}

	if _, err := dst.Write(header); err != nil {
		return 0, "", fmt.Errorf("writing header: %w", err)
	}

	var prefix [prefixLen]byte
	copy(prefix[:], secret[fileKeyLen:])

	h := sha256.New()
	buf := make([]byte, chunkSize)

	var (
		nonce     [nonceLen]byte
		frame     [4]byte
		seq       uint64
		plainSize int64
	)

	for {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			h.Write(buf[:n])
			plainSize += int64(n)

			last := byte(0)
			if rerr != nil {
				last = 1
			}

			ct := aead.Seal(nil, chunkNonce(&nonce, prefix, seq, last), buf[:n], nil)
			binary.BigEndian.PutUint32(frame[:], uint32(len(ct)))

			if _, err := dst.Write(frame[:]); err != nil {
				return 0, "", fmt.Errorf("writing chunk frame: %w", err)
			}

			if _, err := dst.Write(ct); err != nil {
				return 0, "", fmt.Errorf("writing chunk: %w", err)
			}

			seq++
		}

		if rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}

			return 0, "", fmt.Errorf("reading plaintext: %w", rerr)
		}
	}

	binary.BigEndian.PutUint32(frame[:], 0)

	if _, err := dst.Write(frame[:]); err != nil {
		return 0, "", fmt.Errorf("writing terminator: %w", err)
	}

	return plainSize, hex.EncodeToString(h.Sum(nil)), nil
}

// openStream decrypts one sealed stream into dst, returning the
// sender's public key, the plaintext size, and hex SHA-256.
func openStream(recipient key.NodePrivate, src io.Reader, dst io.Writer) (key.NodePublic, int64, string, error) {
	var noSender key.NodePublic

	header := make([]byte, headerLen)
	if _, err := io.ReadFull(src, header); err != nil {
		return noSender, 0, "", fmt.Errorf("%w: reading header: %v", errCorrupt, err)
	}

	var sender tailcat.NodePublic
	if err := sender.UnmarshalBinary(header[:key.NodePublicRawLen]); err != nil {
		return noSender, 0, "", fmt.Errorf("%w: %v", errBadHeader, err)
	}

	secret, ok := recipient.OpenFrom(sender.NodePublic, header[key.NodePublicRawLen:])
	if !ok || len(secret) != fileKeyLen+prefixLen {
		return noSender, 0, "", errBadHeader
	}

	aead, err := chacha20poly1305.NewX(secret[:fileKeyLen])
	if err != nil {
		return noSender, 0, "", err
	}

	var prefix [prefixLen]byte
	copy(prefix[:], secret[fileKeyLen:])

	h := sha256.New()
	ct := make([]byte, chunkSize+aead.Overhead())
	plain := make([]byte, 0, chunkSize)

	var (
		nonce     [nonceLen]byte
		frame     [4]byte
		seq       uint64
		plainSize int64
	)

	for {
		if _, err := io.ReadFull(src, frame[:]); err != nil {
			return noSender, 0, "", fmt.Errorf("%w: missing terminator", errCorrupt)
		}

		n := binary.BigEndian.Uint32(frame[:])
		if n == 0 {
			break
		}

		if n < uint32(aead.Overhead()) || n > uint32(len(ct)) {
			return noSender, 0, "", fmt.Errorf("%w: chunk length %d out of range", errCorrupt, n)
		}

		if _, err := io.ReadFull(src, ct[:n]); err != nil {
			return noSender, 0, "", fmt.Errorf("%w: chunk cut short", errCorrupt)
		}

		pt, err := aead.Open(plain[:0], chunkNonce(&nonce, prefix, seq, 0), ct[:n], nil)
		if err != nil {
			pt, err = aead.Open(plain[:0], chunkNonce(&nonce, prefix, seq, 1), ct[:n], nil)
			if err != nil {
				return noSender, 0, "", errCorrupt
			}
		}

		if _, err := dst.Write(pt); err != nil {
			return noSender, 0, "", fmt.Errorf("writing plaintext: %w", err)
		}

		h.Write(pt)
		plainSize += int64(len(pt))
		seq++
	}

	return sender.NodePublic, plainSize, hex.EncodeToString(h.Sum(nil)), nil
}

// relaySealed copies one sealed stream from src to dst without
// decrypting: the fixed header, then frames until the terminator.
func relaySealed(dst io.Writer, src io.Reader) (int64, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(src, header); err != nil {
		return 0, fmt.Errorf("%w: header cut short", errCorrupt)
	}

	if _, err := dst.Write(header); err != nil {
		return 0, err
	}

	total := int64(headerLen)

	var frame [4]byte

	buf := make([]byte, chunkSize+chacha20poly1305.Overhead)

	for {
		if _, err := io.ReadFull(src, frame[:]); err != nil {
			return 0, fmt.Errorf("%w: missing terminator", errCorrupt)
		}

		n := binary.BigEndian.Uint32(frame[:])
		if n == 0 {
			if _, err := dst.Write(frame[:]); err != nil {
				return 0, err
			}

			return total + 4, nil
		}

		if n > uint32(len(buf)) {
			return 0, fmt.Errorf("%w: chunk length %d out of range", errCorrupt, n)
		}

		if _, err := io.ReadFull(src, buf[:n]); err != nil {
			return 0, fmt.Errorf("%w: chunk cut short", errCorrupt)
		}

		if _, err := dst.Write(frame[:]); err != nil {
			return 0, err
		}

		if _, err := dst.Write(buf[:n]); err != nil {
			return 0, err
		}

		total += 4 + int64(n)
	}
}

// chunkNonce fills nonce with prefix, a big-endian chunk counter, two
// zero bytes, and the last-chunk flag.
func chunkNonce(nonce *[nonceLen]byte, prefix [prefixLen]byte, seq uint64, last byte) []byte {
	copy(nonce[:prefixLen], prefix[:])
	binary.BigEndian.PutUint64(nonce[prefixLen:prefixLen+8], seq)
	nonce[nonceLen-1] = last

	return nonce[:]
}
