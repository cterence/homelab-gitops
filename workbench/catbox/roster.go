package main

// Roster: the storer is the single writer of the member list; peers
// overwrite their cache from every storer response.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"tailscale.com/types/key"
)

func rosterPath(dir string) string { return filepath.Join(dir, "roster.json") }

// loadRoster reads members from path; a missing file is an empty roster.
func loadRoster(path string) ([]member, error) {
	var members []member
	if err := loadJSON(path, &members); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	return members, nil
}

func saveRoster(path string, members []member) error { return saveJSON(path, members) }

func memberByName(members []member, name string) (member, bool) {
	for _, m := range members {
		if m.Name == name {
			return m, true
		}
	}

	return member{}, false
}

// memberByDialKey finds the member a connection belongs to; the dial
// key is what the storer sees via PeerKey.
func memberByDialKey(members []member, k key.NodePublic) (member, bool) {
	for _, m := range members {
		if m.DialKey == k {
			return m, true
		}
	}

	return member{}, false
}

// parseNodeKey parses a node public key's text form.
func parseNodeKey(s string) (key.NodePublic, error) {
	var k key.NodePublic
	if err := k.UnmarshalText([]byte(s)); err != nil {
		return key.NodePublic{}, err
	}

	return k, nil
}

// validName reports whether name is a lowercase slug.
func validName(name string) bool {
	if len(name) == 0 || len(name) > 32 || name[0] == '-' {
		return false
	}

	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}

		return false
	}

	return true
}

// loadJSON reads path into v.
func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return json.Unmarshal(b, v)
}
