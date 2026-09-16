package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string) error
	Delete(ctx context.Context, key string) error
	Close()
}

type InMemory struct {
	cache map[string]string
}

type Redis struct {
	client *redis.Client
}

type File struct {
	mu   sync.Mutex
	file *os.File
	path string
	data map[string]string

	flushCh  chan struct{}
	stopCh   chan struct{}
	interval time.Duration
	wg       sync.WaitGroup
}

var ErrCacheMiss = errors.New("cache miss")

func NewInMemory() Cache {
	return &InMemory{
		cache: make(map[string]string),
	}
}

func (c *InMemory) Get(_ context.Context, key string) (string, error) {
	value, exists := c.cache[key]
	if !exists {
		return "", ErrCacheMiss
	}

	return value, nil
}

func (c *InMemory) Set(_ context.Context, key string, value string) error {
	c.cache[key] = value
	return nil
}

func (c *InMemory) Delete(_ context.Context, key string) error {
	delete(c.cache, key)
	return nil
}

func (c *InMemory) Close() {}

func NewRedis(redisClient *redis.Client) Cache {
	return &Redis{
		client: redisClient,
	}
}

func (c *Redis) Get(ctx context.Context, key string) (string, error) {
	val, err := c.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return "", ErrCacheMiss
		}

		return "", err
	}

	return val, nil
}

func (c *Redis) Set(ctx context.Context, key string, value string) error {
	return c.client.Set(ctx, key, value, 0).Err()
}

func (c *Redis) Close() {
	if err := c.client.Close(); err != nil {
		slog.Error("Failed to close redis client", "error", err)
	}
}

func (c *Redis) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

const cacheFileName = "cache.db"

func NewFile(path string, flushInterval time.Duration) (Cache, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return nil, err
	}

	cache := &File{
		file:     f,
		path:     path,
		data:     make(map[string]string),
		flushCh:  make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
		interval: flushInterval,
	}

	if err := cache.load(); err != nil {
		if err := f.Close(); err != nil {
			return nil, err
		}

		return nil, err
	}

	cache.startFlusher()

	return cache, nil
}

func (c *File) load() error {
	c.data = make(map[string]string)

	f, err := os.Open(c.path)
	if err != nil {
		return err
	}
	defer CloseFile(f)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			c.data[parts[0]] = parts[1]
		}
	}

	return scanner.Err()
}

func (c *File) Get(ctx context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	val, ok := c.data[key]
	if !ok {
		return "", ErrCacheMiss
	}

	return val, nil
}

func (c *File) Set(ctx context.Context, key string, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// update in-memory map
	c.data[key] = value

	return nil
}

func (c *File) Close() {
	// compact before closing to avoid unbounded growth
	if err := c.Flush(); err != nil {
		slog.Error("failed to flush file cache", "error", err)
	}

	if err := c.file.Close(); err != nil {
		slog.Error("failed to close file cache", "error", err)
	}
}

func (c *File) Delete(ctx context.Context, key string) error {
	delete(c.data, key)
	return nil
}

const fileCacheFlushTicker = 30 * time.Second

// Flush compacts the append-only log by rewriting only latest values
func (c *File) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tmpPath := c.path + ".tmp"

	tmpFile, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return err
	}

	for k, v := range c.data {
		if _, err := fmt.Fprintf(tmpFile, "%s=%s\n", k, v); err != nil {
			errClose := tmpFile.Close()
			if errClose != nil {
				return errClose
			}

			return err
		}
	}

	if err := tmpFile.Sync(); err != nil {
		errClose := tmpFile.Close()
		if errClose != nil {
			return errClose
		}

		return err
	}

	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Replace old file atomically
	if err := os.Rename(tmpPath, c.path); err != nil {
		return err
	}

	// reopen file for future operations (not strictly needed now)
	f, err := os.OpenFile(c.path, os.O_RDWR, 0666)
	if err != nil {
		return err
	}

	err = c.file.Close()
	if err != nil {
		return err
	}

	c.file = f

	slog.Debug("Flushed data to cache file")

	return nil
}

func (c *File) startFlusher() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()

		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := c.Flush(); err != nil {
					slog.Error("periodic flush failed", "error", err)
				}
			case <-c.stopCh:
				return
			}
		}
	}()
}
