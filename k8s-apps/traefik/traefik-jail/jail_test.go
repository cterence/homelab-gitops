package main

import (
	"testing"
	"time"
)

func TestJailer_BanDuration(t *testing.T) {
	j := NewJailer(10, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	tests := []struct {
		name     string
		banCount int
		want     time.Duration
	}{
		{"first ban", 1, 1 * time.Minute},
		{"second ban", 2, 2 * time.Minute},
		{"third ban", 3, 4 * time.Minute},
		{"fourth ban", 4, 8 * time.Minute},
		{"fifth ban", 5, 16 * time.Minute},
		{"sixth ban", 6, 32 * time.Minute},
		{"seventh ban", 7, 1 * time.Hour}, // capped
		{"eighth ban", 8, 1 * time.Hour},  // still capped
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := j.banDuration(tt.banCount)
			if got != tt.want {
				t.Errorf("banDuration(%d) = %s, want %s", tt.banCount, got, tt.want)
			}
		})
	}
}

func TestJailer_RecordError(t *testing.T) {
	j := NewJailer(3, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	// Two errors — should not be jailed
	for range 2 {
		d := j.RecordError("1.2.3.4", now)
		if d != 0 {
			t.Fatalf("expected no ban, got %s", d)
		}
	}

	// Third error — should be jailed for 1 minute
	d := j.RecordError("1.2.3.4", now)
	if d != 1*time.Minute {
		t.Fatalf("expected 1m ban, got %s", d)
	}

	// Should be jailed now
	if !j.IsJailed("1.2.3.4", now) {
		t.Fatal("expected IP to be jailed")
	}

	// After ban expires — should not be jailed
	afterBan := now.Add(1 * time.Minute)
	if j.IsJailed("1.2.3.4", afterBan) {
		t.Fatal("expected IP to be unjailed after ban expires")
	}

	// Re-offend: second ban should be 2 minutes
	for range 2 {
		j.RecordError("1.2.3.4", afterBan)
	}

	d = j.RecordError("1.2.3.4", afterBan)
	if d != 2*time.Minute {
		t.Fatalf("expected 2m ban on second offense, got %s", d)
	}
}

func TestJailer_WindowReset(t *testing.T) {
	j := NewJailer(3, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	// Two errors within the window
	j.RecordError("1.2.3.4", now)
	j.RecordError("1.2.3.4", now.Add(10*time.Second))

	// After the window elapses, count should reset
	later := now.Add(61 * time.Second)

	d := j.RecordError("1.2.3.4", later)
	if d != 0 {
		t.Fatalf("expected no ban after window reset, got %s", d)
	}
}

func TestJailer_BanCounterReset(t *testing.T) {
	j := NewJailer(3, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	// First ban (1m)
	for range 3 {
		j.RecordError("1.2.3.4", now)
	}

	// After ban expires + resetAfter, ban counter should reset
	afterReset := now.Add(1 * time.Minute).Add(1 * time.Hour)
	if j.IsJailed("1.2.3.4", afterReset) {
		t.Fatal("expected IP to be unjailed")
	}

	// New ban should be 1m again (counter reset)
	for range 3 {
		j.RecordError("1.2.3.4", afterReset)
	}

	if !j.IsJailed("1.2.3.4", afterReset) {
		t.Fatal("expected IP to be jailed again")
	}

	afterBan := afterReset.Add(1 * time.Minute)
	if j.IsJailed("1.2.3.4", afterBan) {
		t.Fatal("expected 1m ban (reset counter)")
	}
}

func TestJailer_DifferentIPs(t *testing.T) {
	j := NewJailer(3, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	// IP A gets banned
	for range 3 {
		j.RecordError("10.0.0.1", now)
	}

	if !j.IsJailed("10.0.0.1", now) {
		t.Fatal("expected 10.0.0.1 to be jailed")
	}

	// IP B should not be affected
	if j.IsJailed("10.0.0.2", now) {
		t.Fatal("10.0.0.2 should not be jailed")
	}
}

func TestJailer_Only4xxTriggersBan(t *testing.T) {
	j := NewJailer(3, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	// RecordError counts all calls — the plugin decides what's a 4xx.
	// This test just verifies the counting logic.
	for range 3 {
		j.RecordError("10.0.0.1", now)
	}

	if !j.IsJailed("10.0.0.1", now) {
		t.Fatal("expected ban after 3 errors")
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		want       string
	}{
		{
			name:       "X-Forwarded-For single",
			headers:    map[string]string{"X-Forwarded-For": "1.2.3.4"},
			remoteAddr: "10.0.0.1:12345",
			want:       "1.2.3.4",
		},
		{
			name:       "X-Forwarded-For multiple",
			headers:    map[string]string{"X-Forwarded-For": "1.2.3.4, 5.6.7.8"},
			remoteAddr: "10.0.0.1:12345",
			want:       "1.2.3.4",
		},
		{
			name:       "X-Real-Ip fallback",
			headers:    map[string]string{"X-Real-Ip": "1.2.3.4"},
			remoteAddr: "10.0.0.1:12345",
			want:       "1.2.3.4",
		},
		{
			name:       "remote addr fallback",
			headers:    map[string]string{},
			remoteAddr: "10.0.0.1:12345",
			want:       "10.0.0.1",
		},
		{
			name:       "remote addr no port",
			headers:    map[string]string{},
			remoteAddr: "10.0.0.1",
			want:       "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIP(tt.headers, tt.remoteAddr)
			if got != tt.want {
				t.Errorf("extractIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
