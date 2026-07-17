package config

import "testing"

func TestLoadRequiresMandatoryVars(t *testing.T) {
	t.Setenv("ROOTLY_API_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when ROOTLY_API_TOKEN missing")
	}
}

func TestLoadDefaultsAndParsing(t *testing.T) {
	t.Setenv("ROOTLY_API_TOKEN", "tok")
	t.Setenv("ROOTLY_USER_EMAIL", "me@example.com")
	t.Setenv("GCAL_CALENDAR_ID", "cal123")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/creds/key.json")
	t.Setenv("SYNC_DAYS", "")
	t.Setenv("DRY_RUN", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SyncDays != 90 {
		t.Errorf("SyncDays default: want 90 got %d", cfg.SyncDays)
	}
	if !cfg.DryRun {
		t.Errorf("DryRun: want true")
	}
	if cfg.RootlyUserEmail != "me@example.com" {
		t.Errorf("email not parsed")
	}
}
