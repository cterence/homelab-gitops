package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/michiwend/gomusicbrainz"
)

type Config struct {
	// Inputs
	FilePath           string
	CacheType          string
	LastFMUsername     string
	LastFMPassword     string
	CanDelete          bool
	StartPage          int
	From               time.Time
	To                 time.Time
	BrowserHeadful     bool
	RedisURL           string
	BrowserURL         string
	LogLevel           string
	DuplicateThreshold int
	CompleteThreshold  int
	ProcessingMode     string
	DataDir            string
	TelegramBotToken   string
	TelegramChatID     string

	// Internal dependencies
	startTime   time.Time
	cache       Cache
	runStats    stats
	mb          *gomusicbrainz.WS2Client
	taskCtx     context.Context
	telegramBot *bot.Bot

	// Internal variables
	noLogin               bool
	unknownTrackDurations durationByTrackByArtist
	deletedScrobbles      []*scrobble

	// Closing functions
	allocCancel context.CancelFunc
	taskCancel  context.CancelFunc
}

type stats struct {
	cacheHits                      int
	cacheMisses                    int
	processedScrobbles             int
	unknownTrackDurationsCount     int
	skippedScrobbleUnknownDuration int
	scrobbleDeleteFails            int
	elapsedTime                    time.Duration
}

func (c *Config) checkConfig() error {
	slog.Debug("Validating config")

	if c.CacheType == "redis" && c.RedisURL == "" {
		return errors.New("must set redis-url if cache-type is redis")
	}

	if c.StartPage != 0 && (!c.From.IsZero() || !c.To.IsZero()) {
		return errors.New(`start-page and "from" / "to" dates must not be set at the same time`)
	}

	if !c.From.IsZero() && !c.To.IsZero() && c.From.After(c.To) {
		return errors.New(`"to" date must be after "from" date`)
	}

	if c.DuplicateThreshold < 0 || c.DuplicateThreshold > 100 {
		return errors.New("duplicate-threshold must be between 0 and 100")
	}

	if c.CompleteThreshold < 0 || c.CompleteThreshold > 100 {
		return errors.New("complete-threshold must be between 0 and 100")
	}

	if (c.TelegramBotToken != "" && c.TelegramChatID == "") || (c.TelegramBotToken == "" && c.TelegramChatID != "") {
		return errors.New("telegram-bot-token and telegram-chat-id must both be set")
	}

	return nil
}

func (c *Config) close() {
	c.allocCancel()
	c.taskCancel()
	c.cache.Close()
}

func (c *Config) handleInterrupts(ctx context.Context) {
	sigInterrupt := make(chan os.Signal, 1)

	signal.Notify(sigInterrupt, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigInterrupt
		slog.Warn("Closing due to interrupt")

		if err := finishRun(ctx, c); err != nil {
			slog.Error("Failed to finish run", "error", err)
		}

		os.Exit(1)
	}()
}
