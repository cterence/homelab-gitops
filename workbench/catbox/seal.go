package main

// Sealed streams: a header (sender key + sealed box over the file key
// and nonce prefix) followed by length-framed XChaCha20-Poly1305
// chunks, age's STREAM construction. A zero-length frame terminates.

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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

// chunkFrameLen is the wire size of one full plaintext chunk: frame
// length + ciphertext + tag.
const chunkFrameLen = 4 + chunkSize + chacha20poly1305.Overhead

// sealedOffset returns the byte offset of chunk k's frame in a sealed
// stream: valid while every earlier chunk is full-size, which a
// chunk-aligned k guarantees.
func sealedOffset(k int64) int64 {
	return headerLen + k*chunkFrameLen
}

// fileSecret derives the per-file key and nonce prefix instead of
// generating them at random: a resumed attempt must decrypt the
// receiver's existing partial, so the same (sender, recipient, file)
// must always produce the same secret. Overlapping chunk counters
// across attempts reuse nonces for identical plaintext; that equality
// is visible only to the recipient, who already holds the plaintext.
func fileSecret(sender key.NodePrivate, recipient key.NodePublic, shaHex string) [fileKeyLen + prefixLen]byte {
	senderRaw := sender.Raw32()
	recipientRaw := recipient.AppendTo(nil)

	h := sha512.New()
	h.Write([]byte("catbox file secret v1"))
	h.Write(senderRaw[:])
	h.Write(recipientRaw)
	h.Write([]byte(shaHex))

	var secret [fileKeyLen + prefixLen]byte

	sum := h.Sum(nil)
	copy(secret[:], sum[:len(secret)])

	return secret
}

var (
	errBadHeader = errors.New("cannot open sealed header (wrong key or corrupt header)")
	errCorrupt   = errors.New("corrupt or truncated sealed stream")
)

// sealStream seals src to dst for recipient, continuing at plaintext
// offset — a resumed attempt's chunk counter. src must already be
// positioned at that offset. shaHex keys the deterministic file
// secret. It returns the total plaintext size (offset plus what it
// sealed).
func sealStream(sender key.NodePrivate, recipient key.NodePublic, dst io.Writer, src io.Reader, shaHex string, offset int64) (int64, error) {
	secret := fileSecret(sender, recipient, shaHex)

	aead, err := chacha20poly1305.NewX(secret[:fileKeyLen])
	if err != nil {
		return 0, err
	}

	senderRaw, err := tailcat.NodePublic{NodePublic: sender.Public()}.MarshalBinary()
	if err != nil {
		return 0, fmt.Errorf("encoding sender key: %w", err)
	}

	header := append(senderRaw, sender.SealTo(recipient, secret[:])...)
	if len(header) != headerLen {
		return 0, fmt.Errorf("header is %d bytes, want %d", len(header), headerLen)
	}

	if _, err := dst.Write(header); err != nil {
		return 0, fmt.Errorf("writing header: %w", err)
	}

	var prefix [prefixLen]byte
	copy(prefix[:], secret[fileKeyLen:])

	buf := make([]byte, chunkSize)

	var (
		nonce     [nonceLen]byte
		frame     [4]byte
		seq       uint64
		plainSize int64
	)

	if offset > 0 {
		seq = uint64(offset / chunkSize)
		plainSize = offset
	}

	for {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			plainSize += int64(n)

			last := byte(0)
			if rerr != nil {
				last = 1
			}

			ct := aead.Seal(nil, chunkNonce(&nonce, prefix, seq, last), buf[:n], nil)
			binary.BigEndian.PutUint32(frame[:], uint32(len(ct)))

			if _, err := dst.Write(frame[:]); err != nil {
				return 0, fmt.Errorf("writing chunk frame: %w", err)
			}

			if _, err := dst.Write(ct); err != nil {
				return 0, fmt.Errorf("writing chunk: %w", err)
			}

			seq++
		}

		if rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}

			return 0, fmt.Errorf("reading plaintext: %w", rerr)
		}
	}

	binary.BigEndian.PutUint32(frame[:], 0)

	if _, err := dst.Write(frame[:]); err != nil {
		return 0, fmt.Errorf("writing terminator: %w", err)
	}

	return plainSize, nil
}

// openStream decrypts one sealed stream into dst, returning the
// sender's public key, the total plaintext size, and hex SHA-256.
// offset is the chunk-aligned plaintext already on disk (a resumed
// stream starts at that chunk counter); h, when non-nil, is the
// caller's hash state over those bytes so the final SHA covers the
// whole file.
func openStream(recipient key.NodePrivate, src io.Reader, dst io.Writer, offset int64, h hash.Hash) (key.NodePublic, int64, string, error) {
	var noSender key.NodePublic

	if h == nil {
		h = sha256.New()
	}

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

	ct := make([]byte, chunkSize+aead.Overhead())
	plain := make([]byte, 0, chunkSize)

	var (
		nonce     [nonceLen]byte
		frame     [4]byte
		seq       uint64
		plainSize int64
	)

	if offset > 0 {
		seq = uint64(offset / chunkSize)
		plainSize = offset
	}

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
	return relaySealedAt(dst, src, 0)
}

// relaySealedAt is relaySealed resumed at plaintext offset have: the
// deterministic header is consumed from src either way and kept only
// when the copy starts at zero; frames continue at the offset's chunk.
func relaySealedAt(dst io.Writer, src io.Reader, have int64) (int64, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(src, header); err != nil {
		return 0, fmt.Errorf("%w: header cut short", errCorrupt)
	}

	var total int64

	if have == 0 {
		if _, err := dst.Write(header); err != nil {
			return 0, err
		}

		total = headerLen
	}

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
