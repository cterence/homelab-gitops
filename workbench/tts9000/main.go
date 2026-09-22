// tts9000 is a Telegram bot that converts article URLs to audio using
// Mistral's chat and Voxtral TTS APIs.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// mistralTimeout matches the previous Python SDK timeout (300s) to
	// accommodate long articles.
	mistralTimeout = 300 * time.Second
	extractTimeout = 120 * time.Second
	pollTimeout    = 60
)

func run(ctx context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if err := pruneCache(cacheDir, cfg.cacheMaxAgeDays); err != nil {
		logger.Warn("pruning cache", "err", err)
	}

	api, err := tgbotapi.NewBotAPI(cfg.telegramToken)
	if err != nil {
		return fmt.Errorf("connecting to Telegram: %w", err)
	}

	api.Debug = false

	mistral := newMistralClient(
		&http.Client{Timeout: mistralTimeout},
		cfg.mistralAPIKey,
		cfg.mistralAPIBase,
		cfg.systemPromptClean,
		cfg.systemPromptTitle,
	)
	b := &bot{
		api:        api,
		mistral:    mistral,
		httpClient: &http.Client{Timeout: extractTimeout},
		cfg:        cfg,
		logger:     logger,
	}

	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = pollTimeout
	updates := api.GetUpdatesChan(updateConfig)

	logger.Info("bot started", "username", api.Self.UserName)

	for {
		select {
		case <-ctx.Done():
			return nil
		case update := <-updates:
			go b.handleUpdate(ctx, update)
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
