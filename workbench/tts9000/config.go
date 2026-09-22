package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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

// config is the bot configuration, read from the environment at startup.
type config struct {
	telegramToken     string
	mistralAPIKey     string
	allowedUsers      []string
	cacheMaxAgeDays   int
	systemPromptClean string
	systemPromptTitle string
	// mistralAPIBase defaults to the public Mistral API; an env override
	// allows local end-to-end testing against a fake API.
	mistralAPIBase string
}

func loadConfig() (config, error) {
	cfg := config{
		telegramToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		mistralAPIKey:     os.Getenv("MISTRAL_API_KEY"),
		allowedUsers:      parseAllowedUsers(os.Getenv("ALLOWED_USERS")),
		systemPromptClean: envOrDefault("SYSTEM_PROMPT_CLEAN", defaultCleanPrompt),
		systemPromptTitle: envOrDefault("SYSTEM_PROMPT_TITLE", defaultTitlePrompt),
		mistralAPIBase:    mistralAPIBase,
	}

	if cfg.telegramToken == "" || cfg.mistralAPIKey == "" {
		return cfg, errors.New("TELEGRAM_BOT_TOKEN and MISTRAL_API_KEY environment variables must be set")
	}

	maxAge := defaultCacheMaxAgeDays

	if raw := os.Getenv("CACHE_MAX_AGE_DAYS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return cfg, fmt.Errorf("parsing CACHE_MAX_AGE_DAYS %q: %w", raw, err)
		}

		maxAge = parsed
	}

	cfg.cacheMaxAgeDays = maxAge

	if override := os.Getenv("MISTRAL_API_BASE"); override != "" {
		cfg.mistralAPIBase = override
	}

	return cfg, nil
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
