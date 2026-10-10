package main

// Join invites: one-time codes an admin mints. A join must present a
// live code once the roster has members; the first member ever joins
// without one and bootstraps as admin.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// inviteTTL bounds how long a minted code stays usable.
const inviteTTL = 24 * time.Hour

// invite is one unadmitted join code.
type invite struct {
	Code    string `json:"code"`
	Expires int64  `json:"expires"` // unix seconds
}

// inviteJSON is what `catbox invite` prints and `join` takes: the
// one-time code and the storer it was minted on.
type inviteJSON struct {
	Code   string `json:"code"`
	Storer string `json:"storer"`
}

// encodeInvite renders the invite JSON as one base64url token: a
// single paste with no braces or quotes to fight.
func encodeInvite(iv inviteJSON) (string, error) {
	b, err := json.Marshal(iv)
	if err != nil {
		return "", fmt.Errorf("marshaling invite: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// parseInviteArg decodes a join argument that is an invite token.
// Anything else — a storer address, a typo — is an error; the caller
// falls back to treating it as an address.
func parseInviteArg(s string) (inviteJSON, error) {
	var iv inviteJSON

	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return iv, errors.New("not an invite token")
	}

	if err := json.Unmarshal(b, &iv); err != nil {
		return iv, errors.New("not an invite token")
	}

	if iv.Code == "" || iv.Storer == "" {
		return iv, errors.New("invite needs a code and a storer address")
	}

	return iv, nil
}

func invitePath(dir string) string { return filepath.Join(dir, "invites.json") }

// loadInvites reads live invite codes; a missing file is no invites,
// and expired codes are pruned on load.
func loadInvites(path string) ([]invite, error) {
	var invites []invite
	if err := loadJSON(path, &invites); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	return pruneInvites(invites, time.Now().Unix()), nil
}

// pruneInvites drops expired codes.
func pruneInvites(invites []invite, now int64) []invite {
	return slices.DeleteFunc(invites, func(iv invite) bool { return iv.Expires <= now })
}

func saveInvites(path string, invites []invite) error { return saveJSON(path, invites) }

// mintInvite generates a fresh code valid for inviteTTL.
func mintInvite(now time.Time) (invite, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return invite{}, fmt.Errorf("reading randomness: %w", err)
	}

	return invite{Code: hex.EncodeToString(b), Expires: now.Add(inviteTTL).Unix()}, nil
}
