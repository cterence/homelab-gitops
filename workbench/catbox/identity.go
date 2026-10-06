package main

// Storer and peer identities, created on first use, loaded forever after.
// The storer identity is a tailcat.PrivateKey (node key, PSK, region);
// the peer identity is a name, node key, and the storer address.

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

// peerID is a non-storer machine's identity. Key is the identity and
// listener engine key; DialKey drives client engines, which must never
// share a node key with a concurrently running listener. PSK and
// Region exist only once this peer has listened.
type peerID struct {
	Name       string               `json:"name"`
	Key        key.NodePrivate      `json:"key"`
	DialKey    key.NodePrivate      `json:"dial_key"`
	PSK        tailcat.PresharedKey `json:"psk,omitempty"`
	Region     int64                `json:"region,omitempty"`
	StorerAddr tailcat.Addr         `json:"storer_addr"`
}

func peerDir() (string, error) {
	conf, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}

	return filepath.Join(conf, "catbox"), nil
}

// loadPeerID returns the peer identity, creating it on first use.
func loadPeerID(name string, storer tailcat.Addr) (*peerID, bool, error) {
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

		return id, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("reading %s: %w", path, err)
	}

	if name == "" {
		return nil, false, fmt.Errorf("no identity in %s; run join with --name", path)
	}

	id := &peerID{Name: name, Key: key.NewNode(), DialKey: key.NewNode(), StorerAddr: storer}
	if err := saveJSON(path, id); err != nil {
		return nil, false, err
	}

	return id, true, nil
}

// loadStorerIdentity returns the storer identity, creating it on first
// use. Region 0 probes the DERP map once; the choice is baked in so the
// tailcat address never changes. A negative region never creates.
func loadStorerIdentity(ctx context.Context, dataDir string, region int64) (*tailcat.PrivateKey, bool, error) {
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

// saveJSON atomically writes v as 0600 JSON.
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
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
