package traefikjail

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

	// After the window elapses the count halves instead of resetting:
	// 2 errors decay to 1, plus this one = 2, still below the threshold of 3.
	later := now.Add(61 * time.Second)

	d := j.RecordError("1.2.3.4", later)
	if d != 0 {
		t.Fatalf("expected no ban after window decay, got %s", d)
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

func TestJailer_RecordErrorsWeight(t *testing.T) {
	j := NewJailer(10, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	// Two weighted errors of 5 reach the threshold of 10
	if d := j.RecordErrors("1.2.3.4", 5, now); d != 0 {
		t.Fatalf("expected no ban, got %s", d)
	}

	if d := j.RecordErrors("1.2.3.4", 5, now); d != 1*time.Minute {
		t.Fatalf("expected 1m ban, got %s", d)
	}

	// Weight 0 must never accumulate
	afterBan := now.Add(1 * time.Minute)

	for range 10 {
		if d := j.RecordErrors("5.6.7.8", 0, afterBan); d != 0 {
			t.Fatalf("expected no ban with weight 0, got %s", d)
		}
	}

	if j.IsJailed("5.6.7.8", afterBan) {
		t.Fatal("weight 0 must never jail")
	}
}

func TestJailer_DecayWindow(t *testing.T) {
	j := NewJailer(100, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	// 80 errors in the first window
	j.RecordErrors("1.2.3.4", 80, now)

	s := j.ips["1.2.3.4"]
	if s.errorCount != 80 {
		t.Fatalf("initial count = %d, want 80", s.errorCount)
	}

	// One window later: 80 halves to 40, plus this error = 41
	j.RecordError("1.2.3.4", now.Add(61*time.Second))

	if s.errorCount != 41 {
		t.Errorf("count after 1 halving = %d, want 41", s.errorCount)
	}

	if !s.windowStart.Equal(now.Add(60 * time.Second)) {
		t.Errorf("windowStart = %s, want %s (advanced by one window)", s.windowStart, now.Add(60*time.Second))
	}

	// Two more windows later: 41 halves twice (10), plus this error = 11
	j.RecordError("1.2.3.4", now.Add(181*time.Second))

	if s.errorCount != 11 {
		t.Errorf("count after 2 more halvings = %d, want 11", s.errorCount)
	}

	if !s.windowStart.Equal(now.Add(180 * time.Second)) {
		t.Errorf("windowStart = %s, want %s", s.windowStart, now.Add(180*time.Second))
	}

	// Far in the future: the count decays to 0 and the window restarts at now
	much := now.Add(600 * time.Second)
	j.RecordError("1.2.3.4", much)

	if s.errorCount != 1 {
		t.Errorf("count after full decay = %d, want 1", s.errorCount)
	}

	if !s.windowStart.Equal(much) {
		t.Errorf("windowStart = %s, want %s (reset to now)", s.windowStart, much)
	}
}

func TestJailer_DecayHalvingClamp(t *testing.T) {
	// Threshold above the recorded count so no ban fires mid-test
	j := NewJailer(1<<40, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	// A huge count 1000 windows later: halvings are clamped at 31,
	// so 1<<35 decays to 1<<4 = 16, plus this error = 17.
	j.RecordErrors("1.2.3.4", 1<<35, now)
	j.RecordError("1.2.3.4", now.Add(1000*60*time.Second))

	s := j.ips["1.2.3.4"]
	if s.errorCount != 17 {
		t.Errorf("count = %d, want 17 (31-halving clamp)", s.errorCount)
	}
}

func TestJailer_SlowBurnScannerEventuallyBanned(t *testing.T) {
	// The headline scenario: 25 errors per 2-minute window stays below the
	// threshold of 30 forever with a tumbling window, but the decaying
	// window accumulates and bans within a few windows.
	j := NewJailer(30, 2*time.Minute, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	bannedAt := -1

	for w := range 10 {
		ts := now.Add(time.Duration(w) * 2 * time.Minute)

		for range 25 {
			if d := j.RecordError("45.148.10.5", ts); d > 0 {
				bannedAt = w
			}
		}

		if bannedAt >= 0 {
			break
		}
	}

	if bannedAt != 1 {
		t.Fatalf("expected ban in window 1 (12 decayed + 25 = 37 >= 30), bannedAt = %d", bannedAt)
	}
}

func TestJailer_LegitBurstDecaysAway(t *testing.T) {
	// A one-off burst of 20 errors followed by a trickle of 1 per window
	// must never cross the threshold of 30.
	j := NewJailer(30, 60*time.Second, 1*time.Minute, 1*time.Hour, 1*time.Hour)

	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	for w := range 10 {
		weight := 1
		if w == 0 {
			weight = 20
		}

		j.RecordErrors("93.184.216.34", weight, now.Add(time.Duration(w)*60*time.Second))
	}

	if j.IsJailed("93.184.216.34", now.Add(10*60*time.Second)) {
		t.Fatal("legitimate burst should decay away without a ban")
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

func TestIsAllowed(t *testing.T) {
	tests := []struct {
		name      string
		ip        string
		allowList []string
		want      bool
	}{
		{"empty list", "1.2.3.4", nil, false},
		{"single IP match", "1.2.3.4", []string{"1.2.3.4"}, true},
		{"single IP no match", "1.2.3.4", []string{"5.6.7.8"}, false},
		{"CIDR match", "10.0.0.5", []string{"10.0.0.0/8"}, true},
		{"CIDR no match", "11.0.0.5", []string{"10.0.0.0/8"}, false},
		{"mixed list match CIDR", "192.168.1.50", []string{"10.0.0.0/8", "192.168.1.0/24"}, true},
		{"mixed list match IP", "34.75.201.84", []string{"10.0.0.0/8", "34.75.201.84"}, true},
		{"invalid IP", "not-an-ip", []string{"10.0.0.0/8"}, false},
		{"invalid CIDR entry", "1.2.3.4", []string{"invalid"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAllowed(tt.ip, tt.allowList); got != tt.want {
				t.Errorf("isAllowed(%q, %v) = %v, want %v", tt.ip, tt.allowList, got, tt.want)
			}
		})
	}
}
