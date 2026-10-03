package main

import (
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Run("required variables", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "")
		t.Setenv("MISTRAL_API_KEY", "")

		if _, err := loadConfig(); err == nil {
			t.Fatal("loadConfig() error = nil, want missing-credentials error")
		}
	})

	t.Run("defaults", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
		t.Setenv("MISTRAL_API_KEY", "mistral-key")
		t.Setenv("ALLOWED_USERS", "")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}

		if len(cfg.allowedUsers) != 0 {
			t.Errorf("allowedUsers = %v, want empty (allow all)", cfg.allowedUsers)
		}

		if !strings.Contains(cfg.systemPromptClean, "text-to-speech") {
			t.Errorf("default clean prompt missing: %q", cfg.systemPromptClean)
		}
	})

	t.Run("parses allowed users", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
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
