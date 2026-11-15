package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// pairKey is the canonical dedupe key for a flow pair: \x00-joined so it
// matches the legacy seen.json keys byte-for-byte and migrates as-is.
func pairKey(p Pair) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s",
		p.SrcNS, p.SrcPod, p.DstKind, p.DstNS, p.DstPod, p.DstIP, p.Proto, p.DstPort, p.Verdict)
}

func dnsKey(ns, qname string) string {
	return "dns\x00" + ns + "\x00" + qname
}

// Store is the valkey-backed novelty set: a key exists for every record
// ever seen, holding its first-seen unix seconds.
type Store struct {
	rdb *redis.Client
}

func NewStore(addr string) *Store {
	return &Store{rdb: redis.NewClient(&redis.Options{Addr: addr, MaxRetries: 3})}
}

// Wait blocks until valkey answers a ping.
func (s *Store) Wait(ctx context.Context, logger *slog.Logger) error {
	for {
		if err := s.rdb.Ping(ctx).Err(); err == nil {
			return nil
		}

		logger.Warn("waiting for valkey", "addr", s.rdb.Options().Addr)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *Store) Size(ctx context.Context) (int64, error) {
	return s.rdb.DBSize(ctx).Result()
}

// Novel records the key at its first-seen time and reports whether it was
// new.
func (s *Store) Novel(ctx context.Context, key string, at time.Time) (bool, error) {
	return s.rdb.SetNX(ctx, key, at.Unix(), 0).Result()
}

func (s *Store) Close() error {
	return s.rdb.Close()
}

// Migrate uploads the legacy seen.json into the store (the keys are
// already the canonical mapping) and renames the file so it loads only
// once. A missing file is not an error.
func (s *Store) Migrate(ctx context.Context, path string) (int, error) {
	keys, err := readLegacy(path)
	if err != nil || keys == nil {
		return 0, err
	}

	pipe := s.rdb.Pipeline()
	i := 0

	for k, ts := range keys {
		pipe.SetNX(ctx, k, ts.Unix(), 0)

		if i++; i%500 == 0 {
			if _, err := pipe.Exec(ctx); err != nil {
				return 0, fmt.Errorf("uploading legacy state: %w", err)
			}
		}
	}

	if i%500 != 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			return 0, fmt.Errorf("uploading legacy state: %w", err)
		}
	}

	renamed := path + ".migrated"
	if err := os.Rename(path, renamed); err != nil {
		return 0, fmt.Errorf("renaming migrated state to %s: %w", renamed, err)
	}

	return len(keys), nil
}

// readLegacy parses the pre-valkey JSON state; a nil map means no file.
func readLegacy(path string) (map[string]time.Time, error) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("reading legacy state %s: %w", path, err)
	}

	keys, err := parseLegacy(data)
	if err != nil {
		return nil, fmt.Errorf("parsing legacy state %s: %w", path, err)
	}

	return keys, nil
}

func parseLegacy(data []byte) (map[string]time.Time, error) {
	var keys map[string]time.Time
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("parsing legacy state: %w", err)
	}

	return keys, nil
}
