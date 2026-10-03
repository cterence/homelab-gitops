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

type File struct {
	mu   sync.Mutex
	path string
	data map[string]string
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

const cacheFileName = "cache.db"

func NewFile(path string) (Cache, error) {
	cache := &File{
		path: path,
		data: make(map[string]string),
	}

	if err := cache.load(); err != nil {
		return nil, err
	}

	return cache, nil
}

func (c *File) load() error {
	c.data = make(map[string]string)

	f, err := os.Open(c.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

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
}

func (c *File) Delete(ctx context.Context, key string) error {
	delete(c.data, key)
	return nil
}

// Flush rewrites the cache file with only latest values
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

	slog.Debug("Flushed data to cache file")

	return nil
}
