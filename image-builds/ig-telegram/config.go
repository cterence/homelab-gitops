// Package config holds environment-based configuration for ig-telegram.
// It reuses the same credential keys as tts9000 (TELEGRAM_BOT_TOKEN,
// TELEGRAM_CHAT_ID, ALLOWED_USERS) so the same ExternalSecret can be mounted.
package main

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	TelegramBotToken string
	TelegramChatID   int64
	AllowedUserID    int64
	TemporalAddress  string
	TemporalNamespace string
	TaskQueue        string
}

// LoadConfig reads environment variables without hard-failing on missing
// values. Per-subcommand validation is done by ValidateForBot / ValidateForCLI.
func LoadConfig() Config {
	cfg := Config{
		TelegramBotToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		TemporalAddress:   env("TEMPORAL_ADDRESS", "temporal-frontend:7233"),
		TemporalNamespace: env("TEMPORAL_NAMESPACE", "default"),
		TaskQueue:         env("TASK_QUEUE", "ig-telegram"),
	}
	if v := os.Getenv("TELEGRAM_CHAT_ID"); v != "" {
		cfg.TelegramChatID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := os.Getenv("ALLOWED_USERS"); v != "" {
		cfg.AllowedUserID, _ = strconv.ParseInt(v, 10, 64)
	}
	return cfg
}

// ValidateForBot checks the fields required by the bot polling loop.
func (c Config) ValidateForBot() error {
	if c.TelegramBotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if c.TelegramChatID == 0 {
		return fmt.Errorf("TELEGRAM_CHAT_ID is required and non-zero")
	}
	if c.AllowedUserID == 0 {
		return fmt.Errorf("ALLOWED_USERS is required and non-zero")
	}
	return nil
}

// ValidateForCLI checks the fields required by the post/profile subcommands.
// Telegram credentials are optional — without them, media is downloaded
// but not sent (the file path is logged instead).
func (c Config) ValidateForCLI() error {
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
