package main

import (
	"bytes"
	"encoding/binary"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeFLAC creates a minimal metadata-only FLAC file with the given tags,
// so album grouping can be exercised against real taglib parsing.
func writeFLAC(t *testing.T, dir, name string, tags map[string]string) string {
	t.Helper()

	var buf bytes.Buffer
	buf.WriteString("fLaC")

	// STREAMINFO block: mandatory first block, 34 bytes.
	streamInfo := make([]byte, 34)
	binary.BigEndian.PutUint16(streamInfo[0:2], 4096) // min block size
	binary.BigEndian.PutUint16(streamInfo[2:4], 4096) // max block size
	// 20 bits sample rate | 3 bits channels-1 | 5 bits bps-1 | 36 bits total samples
	packed := uint64(44100)<<44 | uint64(1)<<41 | uint64(15)<<36
	binary.BigEndian.PutUint64(streamInfo[10:18], packed)
	writeFLACBlock(&buf, 0, false, streamInfo)

	// VORBIS_COMMENT block: vendor string + key=value comments, little-endian lengths.
	var vc bytes.Buffer

	vendor := []byte("test")
	vc.Write(uint32le(len(vendor)))
	vc.Write(vendor)

	keys := slices.Sorted(maps.Keys(tags))
	vc.Write(uint32le(len(keys)))

	for _, key := range keys {
		entry := []byte(key + "=" + tags[key])
		vc.Write(uint32le(len(entry)))
		vc.Write(entry)
	}

	writeFLACBlock(&buf, 4, true, vc.Bytes())

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0666); err != nil {
		t.Fatalf("failed to write flac fixture: %v", err)
	}

	return path
}

func uint32le(value int) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(value))

	return b
}

func writeFLACBlock(buf *bytes.Buffer, blockType byte, last bool, data []byte) {
	header := blockType
	if last {
		header |= 0x80
	}

	buf.WriteByte(header)
	buf.Write([]byte{byte(len(data) >> 16), byte(len(data) >> 8), byte(len(data))})
	buf.Write(data)
}

func TestBuildAlbumsSeparatesSameTitleDifferentArtists(t *testing.T) {
	dir := t.TempDir()
	first := writeFLAC(t, dir, "first.flac", map[string]string{
		"ARTIST": "Artist One", "ALBUM": "Greatest Hits", "TITLE": "Song", "TRACKNUMBER": "1", "DATE": "2020",
	})
	second := writeFLAC(t, dir, "second.flac", map[string]string{
		"ARTIST": "Artist Two", "ALBUM": "Greatest Hits", "TITLE": "Song", "TRACKNUMBER": "1", "DATE": "2021",
	})

	albums, err := buildAlbums([]string{first, second}, nil)
	if err != nil {
		t.Fatalf("buildAlbums unexpected error: %v", err)
	}

	if len(albums) != 2 {
		t.Fatalf("expected 2 albums for identically titled albums by different artists, got %d: %v", len(albums), albums)
	}

	for key, a := range albums {
		if len(a.tracks) != 1 {
			t.Errorf("album %q: track count = %d, want 1", key, len(a.tracks))
		}
	}
}

func TestBuildAlbumsGroupsSameAlbum(t *testing.T) {
	dir := t.TempDir()
	first := writeFLAC(t, dir, "first.flac", map[string]string{
		"ARTIST": "Artist One", "ALBUM": "Greatest Hits", "TITLE": "Song A", "TRACKNUMBER": "1", "DATE": "2020",
	})
	second := writeFLAC(t, dir, "second.flac", map[string]string{
		"ARTIST": "Artist One", "ALBUM": "Greatest Hits", "TITLE": "Song B", "TRACKNUMBER": "2", "DATE": "2020",
	})

	albums, err := buildAlbums([]string{first, second}, nil)
	if err != nil {
		t.Fatalf("buildAlbums unexpected error: %v", err)
	}

	if len(albums) != 1 {
		t.Fatalf("expected 1 album, got %d", len(albums))
	}

	for _, a := range albums {
		if len(a.tracks) != 2 {
			t.Errorf("track count = %d, want 2", len(a.tracks))
		}
	}
}
