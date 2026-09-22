package main

import (
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Run("defaults api base to telegram", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
		t.Setenv("TELEGRAM_CHAT_ID", "chat")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}

		if cfg.apiBase != telegramAPIBase {
			t.Errorf("apiBase = %q, want %q", cfg.apiBase, telegramAPIBase)
		}
	})

	t.Run("overrides api base for local testing", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
		t.Setenv("TELEGRAM_CHAT_ID", "chat")
		t.Setenv("TELEGRAM_API_BASE", "http://127.0.0.1:9999")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}

		if cfg.apiBase != "http://127.0.0.1:9999" {
			t.Errorf("apiBase = %q, want http://127.0.0.1:9999", cfg.apiBase)
		}
	})

	t.Run("fails fast without credentials", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "")
		t.Setenv("TELEGRAM_CHAT_ID", "")

		_, err := loadConfig()
		if err == nil {
			t.Fatal("loadConfig() error = nil, want missing-credentials error")
		}
	})
}
