package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	RootlyToken     string
	RootlyUserEmail string
	GCalCalendarID  string
	GoogleCredsFile string
	SyncDays        int
	DryRun          bool
}

func Load() (Config, error) {
	c := Config{
		RootlyToken:     os.Getenv("ROOTLY_API_TOKEN"),
		RootlyUserEmail: os.Getenv("ROOTLY_USER_EMAIL"),
		GCalCalendarID:  os.Getenv("GCAL_CALENDAR_ID"),
		GoogleCredsFile: os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
		SyncDays:        90,
	}
	for k, v := range map[string]string{
		"ROOTLY_API_TOKEN":               c.RootlyToken,
		"ROOTLY_USER_EMAIL":              c.RootlyUserEmail,
		"GCAL_CALENDAR_ID":               c.GCalCalendarID,
		"GOOGLE_APPLICATION_CREDENTIALS": c.GoogleCredsFile,
	} {
		if v == "" {
			return Config{}, fmt.Errorf("missing required env %s", k)
		}
	}
	if s := os.Getenv("SYNC_DAYS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("invalid SYNC_DAYS %q", s)
		}
		c.SyncDays = n
	}
	c.DryRun = strings.EqualFold(os.Getenv("DRY_RUN"), "true")
	return c, nil
}
