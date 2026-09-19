package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v7"
	"github.com/chromedp/chromedp"
	"github.com/go-telegram/bot"
	"github.com/michiwend/gomusicbrainz"
	"github.com/redis/go-redis/v9"
)

func initApp(ctx context.Context, c *Config) error {
	c.startTime = time.Now()

	switch c.CacheType {
	case "redis":
		slog.Info("Using Redis cache")

		redisURLParts, err := url.Parse(c.RedisURL)
		if err != nil {
			return fmt.Errorf("failed to parse Redis URL: %w", err)
		}

		redisPassword, _ := redisURLParts.User.Password()

		redisDB, err := strconv.Atoi(strings.Split(redisURLParts.Path, "/")[1])
		if err != nil {
			return fmt.Errorf("failed to extract Redis DB from URL: %w", err)
		}

		rdb := redis.NewClient(&redis.Options{
			Addr:     redisURLParts.Host,
			Username: redisURLParts.User.Username(),
			Password: redisPassword,
			DB:       redisDB,
		})

		var redisPingTrialCount int

		_, err = backoff.Retry(ctx, func() (struct{}, error) {
			err := rdb.Ping(ctx).Err()
			if err != nil {
				redisPingTrialCount++
				slog.Debug("failed to connect to redis", "error", err, "trial-count", redisPingTrialCount)
			}

			return struct{}{}, err
		}, backoff.WithBackOff(backoff.NewConstantBackOff(3*time.Second)), backoff.WithMaxTries(10))
		if err != nil {
			return fmt.Errorf("failed to connect to Redis: %w", err)
		}

		c.cache = NewRedis(rdb)
	case "file":
		slog.Info("Using file cache")

		fileCache, err := NewFile(path.Join(c.DataDir, cacheFileName), fileCacheFlushTicker)
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
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", !c.BrowserHeadful),
		)
		allocCtx, allocCancel = chromedp.NewExecAllocator(ctx, opts...)
	}

	c.allocCancel = allocCancel

	taskCtx, taskCancel := chromedp.NewContext(
		allocCtx,
		chromedp.WithLogf(log.Printf),
	)

	slog.Info("Starting browser")
	// ensure that the browser process is started
	err = chromedp.Run(taskCtx)
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
