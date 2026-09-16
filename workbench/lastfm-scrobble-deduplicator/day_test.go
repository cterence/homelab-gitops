package main

import (
	"testing"
	"time"
)

func TestParseDay(t *testing.T) {
	now := time.Date(2026, 9, 16, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		name    string
		value   string
		want    time.Time
		wantErr bool
	}{
		{name: "empty yields zero time", value: "", want: time.Time{}},
		{name: "yesterday", value: "yesterday", want: now.AddDate(0, 0, -1)},
		{name: "today", value: "today", want: now},
		{name: "relative values are case insensitive", value: "Yesterday", want: now.AddDate(0, 0, -1)},
		{name: "date", value: "01-03-2025", want: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)},
		{name: "invalid day", value: "32-01-2025", wantErr: true},
		{name: "invalid month", value: "01-13-2025", wantErr: true},
		{name: "wrong layout", value: "2025-03-01", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDay(tt.value, now)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDay(%q) expected error, got %v", tt.value, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("parseDay(%q) unexpected error: %v", tt.value, err)
			}

			if !got.Equal(tt.want) {
				t.Errorf("parseDay(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
