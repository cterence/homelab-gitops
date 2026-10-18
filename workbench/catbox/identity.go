package main

// Stash and peer identities, created on first use, loaded forever after.
// The stash identity is a tailcat.PrivateKey (node key, PSK, region);
// the peer identity is a name, node key, and the stash address.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// peerID is a non-stash machine's identity. Key is the identity and
// listener engine key; DialKey drives client engines, which must never
// share a node key with a concurrently running listener. PSK and
// Region exist only once this peer has listened.
type peerID struct {
	Name      string               `json:"name"`
	Key       key.NodePrivate      `json:"key"`
	DialKey   key.NodePrivate      `json:"dial_key"`
	PSK       tailcat.PresharedKey `json:"psk,omitempty"`
	Region    int64                `json:"region,omitempty"`
	StashAddr tailcat.Addr         `json:"storer_addr"`
}

func peerDir() (string, error) {
	conf, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}

	return filepath.Join(conf, "catbox"), nil
}

// defaultDataDir is the stash data dir on a plain host: its own
// subdir of the config root, so the stash identity never collides
// with this machine's peer identity.
func defaultDataDir() (string, error) {
	dir, err := peerDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "stash"), nil
}

// resolveDataDir picks the data dir: the explicit flag, else the
// CATBOX_DATA env, else the host default (~/.config/catbox/stash on
// Linux, ~/Library/Application Support/catbox/stash on macOS).
func resolveDataDir(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}

	if v := os.Getenv("CATBOX_DATA"); v != "" {
		return v, nil
	}

	return defaultDataDir()
}

// loadPeerID returns the peer identity, creating it on first use.
func loadPeerID(name string, stash tailcat.Addr) (*peerID, bool, error) {
	dir, err := peerDir()
	if err != nil {
		return nil, false, err
	}

	path := filepath.Join(dir, "identity.json")
	if b, err := os.ReadFile(path); err == nil {
		id := new(peerID)
		if err := json.Unmarshal(b, id); err != nil {
			return nil, false, fmt.Errorf("parsing %s: %w", path, err)
		}

		if id.DialKey.IsZero() {
			id.DialKey = key.NewNode()
			if err := saveJSON(path, id); err != nil {
				return nil, false, err
			}
		}

		id.StashAddr = resolvedStash(id.StashAddr)

		return id, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("reading %s: %w", path, err)
	}

	if name == "" {
		return nil, false, fmt.Errorf("no identity in %s; run join with --name", path)
	}

	if !validName(name) {
		return nil, false, fmt.Errorf("invalid member name %q: use a lowercase slug", name)
	}

	id := &peerID{Name: name, Key: key.NewNode(), DialKey: key.NewNode(), StashAddr: stash}
	if err := saveJSON(path, id); err != nil {
		return nil, false, err
	}

	id.StashAddr = resolvedStash(stash)

	return id, true, nil
}

// resolveStash embeds the DERP relay details in a stash address so
// dials skip the DERP map fetch. Best-effort: the unresolved address
// is returned when the map is unreachable, and the next load retries.
func resolveStash(addr tailcat.Addr) tailcat.Addr {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ra, err := addr.Resolve(ctx)
	if err != nil {
		return addr
	}

	return ra
}

// stashAddrCache pairs the raw stash address a resolution was made
// from with its relay-embedded form, so a cache can never be served
// to a different stash.
type stashAddrCache struct {
	Raw      tailcat.Addr `json:"raw"`
	Resolved tailcat.Addr `json:"resolved"`
}

// resolvedStash returns the stash address with its DERP relay
// embedded: the cached resolution when it matches the raw address,
// else a fresh one, cached. The raw address stays the source of
// truth in identity.json, so a stale cache is always recoverable.
func resolvedStash(raw tailcat.Addr) tailcat.Addr {
	dir, err := peerDir()
	if err != nil {
		return raw
	}

	path := filepath.Join(dir, "stash-resolved.json")
	if b, err := os.ReadFile(path); err == nil {
		var c stashAddrCache
		if json.Unmarshal(b, &c) == nil && c.Raw == raw && c.Resolved != "" {
			return c.Resolved
		}
	}

	ra := resolveStash(raw)
	if ra == raw {
		return raw // unresolved: the dial fetches the map itself
	}

	_ = saveJSON(path, stashAddrCache{Raw: raw, Resolved: ra}) // cache only

	return ra
}

// unresolveStash drops the resolved-address cache after a failed
// dial: the relay details may be stale, and the next command
// re-resolves them from the raw address.
func unresolveStash() {
	dir, err := peerDir()
	if err != nil {
		return
	}

	_ = os.Remove(filepath.Join(dir, "stash-resolved.json"))
}

// resetPeer deletes the local identity and roster cache: the device
// is new again and can join a different stash. The old entry stays
// in the stash's roster until a member runs `catbox remove`.
func resetPeer() error {
	dir, err := peerDir()
	if err != nil {
		return err
	}

	for _, name := range []string{"identity.json", "roster.json", "stash-resolved.json"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", name, err)
		}
	}

	return nil
}

// loadStashIdentity returns the stash identity, creating it on first
// use. Region 0 probes the DERP map once; the choice is baked in so the
// tailcat address never changes. A negative region never creates.
func loadStashIdentity(ctx context.Context, dataDir string, region int64) (*tailcat.PrivateKey, bool, error) {
	path := filepath.Join(dataDir, "identity.json")
	if b, err := os.ReadFile(path); err == nil {
		id := new(tailcat.PrivateKey)
		if err := json.Unmarshal(b, id); err != nil {
			return nil, false, fmt.Errorf("parsing %s: %w", path, err)
		}

		return id, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("reading %s: %w", path, err)
	}

	if region < 0 {
		return nil, false, fmt.Errorf("no identity in %s; run serve once first", path)
	}

	id := tailcat.NewPrivateKey()

	if region == 0 {
		picked, perr := pickRegion(ctx)
		if perr != nil {
			return nil, false, fmt.Errorf("picking DERP region: %w", perr)
		}

		region = picked
	}

	id.Public.RegionID = tailcfg.DERPRegionID(region)
	if err := saveJSON(path, id); err != nil {
		return nil, false, err
	}

	return id, true, nil
}

func pickRegion(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tailcat.DefaultDERPMapURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetching DERP map: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("DERP map: status %s", resp.Status)
	}

	dm := new(tailcfg.DERPMap)
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(dm); err != nil {
		return 0, fmt.Errorf("decoding DERP map: %w", err)
	}

	id, err := tailcat.PickBestRegion(ctx, dm)
	if err != nil {
		return 0, err
	}

	return int64(id), nil
}

// saveJSON atomically writes v as minified 0600 JSON.
func saveJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("temp file in %s: %w", filepath.Dir(path), err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("renaming %s: %w", path, err)
	}

	return nil
}
