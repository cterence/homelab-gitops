package main

import (
	"os"
	"strings"
)

const (
	defaultCleanPrompt = "Your job is to process text that will be fed to a text-to-speech model." +
		"Remove all headers, footers, navigation menus, advertisements, image attributions " +
		"and any non-article content from the following text. " +
		"Return only the clean article text. " +
		"Do not alter the text in any way (summarizing, adding parts, reformulating...):"

	defaultTitlePrompt = "Extract the main title or headline from the following article text. " +
		"Return only the title:"

	defaultCacheMaxAgeDays = 30
)

// config is the app configuration, read from the environment at startup.
type config struct {
	botToken          string
	chatID            string
	mistralAPIKey     string
	allowedUsers      []string
	systemPromptClean string
	// apiBase is the public Telegram API; tests inject a fake directly.
	apiBase string

	mistralAPIBase string
	chatCleanModel string
	chatTitleModel string
	ttsModel       string
	voiceEnglish   string
	voiceFrench    string
}

// loadConfig reads the app configuration from the environment. Serve starts
// with whatever is set and degrades at use: the bot loop needs botEnabled,
// the MCP tools fail at call time when their credentials are missing.
func loadConfig() config {
	return config{
		botToken:          os.Getenv("TELEGRAM_BOT_TOKEN"),
		chatID:            os.Getenv("TELEGRAM_CHAT_ID"),
		mistralAPIKey:     os.Getenv("MISTRAL_API_KEY"),
		allowedUsers:      parseAllowedUsers(os.Getenv("ALLOWED_USERS")),
		systemPromptClean: envOrDefault("SYSTEM_PROMPT_CLEAN", defaultCleanPrompt),
		apiBase:           telegramAPIBase,
		mistralAPIBase:    envOrDefault("MISTRAL_API_BASE", mistralAPIBase),
		chatCleanModel:    envOrDefault("MISTRAL_CHAT_CLEAN_MODEL", chatCleanModel),
		chatTitleModel:    envOrDefault("MISTRAL_CHAT_TITLE_MODEL", chatTitleModel),
		ttsModel:          envOrDefault("MISTRAL_TTS_MODEL", ttsModel),
		voiceEnglish:      envOrDefault("MISTRAL_VOICE_EN", voiceEnglish),
		voiceFrench:       envOrDefault("MISTRAL_VOICE_FR", voiceFrench),
	}
}

// botEnabled reports whether the long-polling bot can run: it needs a token
// and at least one allowed user (isAllowedUser fails closed otherwise).
func botEnabled(cfg config) bool {
	return cfg.botToken != "" && len(cfg.allowedUsers) > 0
}

func parseAllowedUsers(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var users []string

	for _, user := range strings.Split(raw, ",") {
		if u := strings.TrimSpace(user); u != "" {
			users = append(users, u)
		}
	}

	return users
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
