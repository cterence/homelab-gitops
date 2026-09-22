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
		t.Setenv("CACHE_MAX_AGE_DAYS", "")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}

		if cfg.cacheMaxAgeDays != 30 {
			t.Errorf("cacheMaxAgeDays = %d, want 30", cfg.cacheMaxAgeDays)
		}

		if cfg.mistralAPIBase != mistralAPIBase {
			t.Errorf("mistralAPIBase = %q, want %q", cfg.mistralAPIBase, mistralAPIBase)
		}

		if len(cfg.allowedUsers) != 0 {
			t.Errorf("allowedUsers = %v, want empty (allow all)", cfg.allowedUsers)
		}

		if !strings.Contains(cfg.systemPromptClean, "text-to-speech") {
			t.Errorf("default clean prompt missing: %q", cfg.systemPromptClean)
		}

		if !strings.Contains(cfg.systemPromptTitle, "main title") {
			t.Errorf("default title prompt missing: %q", cfg.systemPromptTitle)
		}
	})

	t.Run("parses allowed users and cache age", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
		t.Setenv("MISTRAL_API_KEY", "mistral-key")
		t.Setenv("ALLOWED_USERS", "111,222")
		t.Setenv("CACHE_MAX_AGE_DAYS", "7")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}

		if len(cfg.allowedUsers) != 2 || cfg.allowedUsers[0] != "111" || cfg.allowedUsers[1] != "222" {
			t.Errorf("allowedUsers = %v, want [111 222]", cfg.allowedUsers)
		}

		if cfg.cacheMaxAgeDays != 7 {
			t.Errorf("cacheMaxAgeDays = %d, want 7", cfg.cacheMaxAgeDays)
		}
	})

	t.Run("invalid cache age fails", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
		t.Setenv("MISTRAL_API_KEY", "mistral-key")
		t.Setenv("CACHE_MAX_AGE_DAYS", "not-a-number")

		if _, err := loadConfig(); err == nil {
			t.Fatal("loadConfig() error = nil, want parse error")
		}
	})

	t.Run("api base override", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
		t.Setenv("MISTRAL_API_KEY", "mistral-key")
		t.Setenv("MISTRAL_API_BASE", "http://127.0.0.1:9999")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}

		if cfg.mistralAPIBase != "http://127.0.0.1:9999" {
			t.Errorf("mistralAPIBase = %q, want http://127.0.0.1:9999", cfg.mistralAPIBase)
		}
	})
}
