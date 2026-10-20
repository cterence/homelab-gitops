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

		cfg := loadConfig()

		if cfg.apiBase != telegramAPIBase {
			t.Errorf("apiBase = %q, want %q", cfg.apiBase, telegramAPIBase)
		}
	})

	t.Run("starts with an empty environment", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "")
		t.Setenv("TELEGRAM_CHAT_ID", "")
		t.Setenv("MISTRAL_API_KEY", "")
		t.Setenv("ALLOWED_USERS", "")

		cfg := loadConfig()

		if cfg.botToken != "" || cfg.chatID != "" || cfg.mistralAPIKey != "" {
			t.Errorf("loadConfig() = %+v, want zero values", cfg)
		}

		if cfg.systemPromptClean != defaultCleanPrompt {
			t.Errorf("systemPromptClean = %q, want the default prompt", cfg.systemPromptClean)
		}
	})

	t.Run("mistral settings from env", func(t *testing.T) {
		t.Setenv("MISTRAL_API_BASE", "http://localhost:9999")
		t.Setenv("MISTRAL_CHAT_CLEAN_MODEL", "clean-model")
		t.Setenv("MISTRAL_CHAT_TITLE_MODEL", "title-model")
		t.Setenv("MISTRAL_TTS_MODEL", "tts-model")
		t.Setenv("MISTRAL_VOICE_EN", "voice-en")
		t.Setenv("MISTRAL_VOICE_FR", "voice-fr")

		cfg := loadConfig()

		if cfg.mistralAPIBase != "http://localhost:9999" ||
			cfg.chatCleanModel != "clean-model" ||
			cfg.chatTitleModel != "title-model" ||
			cfg.ttsModel != "tts-model" ||
			cfg.voiceEnglish != "voice-en" ||
			cfg.voiceFrench != "voice-fr" {
			t.Errorf("loadConfig() mistral settings = %+v, want the env values", cfg)
		}
	})

	t.Run("parses allowed users", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token")
		t.Setenv("TELEGRAM_CHAT_ID", "chat")
		t.Setenv("MISTRAL_API_KEY", "mistral-key")
		t.Setenv("ALLOWED_USERS", "111,222")

		cfg := loadConfig()

		if len(cfg.allowedUsers) != 2 || cfg.allowedUsers[0] != "111" || cfg.allowedUsers[1] != "222" {
			t.Errorf("allowedUsers = %v, want [111 222]", cfg.allowedUsers)
		}
	})
}

func TestBotEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  config
		want bool
	}{
		{"token and users present", config{botToken: "tok", allowedUsers: []string{"111"}}, true},
		{"no token", config{allowedUsers: []string{"111"}}, false},
		{"no allowed users", config{botToken: "tok"}, false},
		{"nothing set", config{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := botEnabled(tt.cfg); got != tt.want {
				t.Errorf("botEnabled(%+v) = %v, want %v", tt.cfg, got, tt.want)
			}
		})
	}
}
