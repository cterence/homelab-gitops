package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"path"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/go-telegram/bot"
	"github.com/michiwend/gomusicbrainz"
)

func initApp(ctx context.Context, c *Config) error {
	c.startTime = time.Now()

	switch c.CacheType {
	case "file":
		slog.Info("Using file cache")

		fileCache, err := NewFile(path.Join(c.DataDir, cacheFileName))
		if err != nil {
			return fmt.Errorf("failed to create file cache: %w", err)
		}

		c.cache = fileCache
	case "inmemory":
		slog.Info("Using in-memory cache")

		c.cache = NewInMemory()
	default:
		return fmt.Errorf("unsupported cache type: %s", c.CacheType)
	}

	mb, err := gomusicbrainz.NewWS2Client("https://musicbrainz.org", "lastfm-scrobble-deduplicator", "1.0", "https://github.com/cterence")
	if err != nil {
		return fmt.Errorf("failed to create MusicBrainz client: %w", err)
	}

	c.mb = mb

	var (
		allocCtx    context.Context
		allocCancel context.CancelFunc
	)
	if c.BrowserURL != "" {
		allocCtx, allocCancel = chromedp.NewRemoteAllocator(ctx, c.BrowserURL, chromedp.NoModifyURL)
	} else {
		opts := chromedp.DefaultExecAllocatorOptions[:]
		if c.BrowserHeadful {
			opts = append(opts, chromedp.VisibleWindow)
		}
		allocCtx, allocCancel = chromedp.NewExecAllocator(ctx, opts...)
	}

	c.allocCancel = allocCancel

	taskCtx, taskCancel := chromedp.NewContext(
		allocCtx,
		chromedp.WithLogf(log.Printf),
	)

	slog.Info("Starting browser")
	// ensure that the browser process is started
	err = chromedp.Do(taskCtx)
	if err != nil {
		return fmt.Errorf("failed to start browser: %w", err)
	}

	if c.TelegramBotToken != "" {
		b, err := bot.New(c.TelegramBotToken)
		if err != nil {
			return fmt.Errorf("failed to init telegram bot: %w", err)
		}

		c.telegramBot = b
	}

	c.taskCtx = taskCtx
	c.taskCancel = taskCancel

	return nil
}
