package main

import (
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Run("defaults api base to telegram", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
		t.Setenv("TELEGRAM_CHAT_ID", "chat")
		t.Setenv("MISTRAL_API_KEY", "key")
		t.Setenv("ALLOWED_USERS", "111")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}

		if cfg.apiBase != telegramAPIBase {
			t.Errorf("apiBase = %q, want %q", cfg.apiBase, telegramAPIBase)
		}
	})

	t.Run("fails fast without credentials", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "")
		t.Setenv("TELEGRAM_CHAT_ID", "")
		t.Setenv("MISTRAL_API_KEY", "")
		t.Setenv("ALLOWED_USERS", "")

		_, err := loadConfig()
		if err == nil {
			t.Fatal("loadConfig() error = nil, want missing-credentials error")
		}
	})

	t.Run("empty allowed users fails closed", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
		t.Setenv("TELEGRAM_CHAT_ID", "chat")
		t.Setenv("MISTRAL_API_KEY", "mistral-key")
		t.Setenv("ALLOWED_USERS", "")

		if _, err := loadConfig(); err == nil {
			t.Fatal("loadConfig() error = nil, want missing-ALLOWED_USERS error")
		}
	})

	t.Run("parses allowed users", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
		t.Setenv("TELEGRAM_CHAT_ID", "chat")
		t.Setenv("MISTRAL_API_KEY", "mistral-key")
		t.Setenv("ALLOWED_USERS", "111,222")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}

		if len(cfg.allowedUsers) != 2 || cfg.allowedUsers[0] != "111" || cfg.allowedUsers[1] != "222" {
			t.Errorf("allowedUsers = %v, want [111 222]", cfg.allowedUsers)
		}
	})
}
