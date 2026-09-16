package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestGetTrackDurationInvalidCustomDuration(t *testing.T) {
	c := &Config{cache: NewInMemory()}
	s := &scrobble{artist: "Artist", track: "Track"}
	userTrackDurations := durationByTrackByArtist{"Artist": {"Track": "not-a-duration"}}

	err := getTrackDuration(context.Background(), c, userTrackDurations, s)
	if err == nil {
		t.Fatal("getTrackDuration expected error for malformed custom duration, got nil")
	}

	if !strings.Contains(err.Error(), "invalid duration") {
		t.Errorf("error = %v, want invalid duration error", err)
	}

	if s.trackDuration != 0 {
		t.Errorf("trackDuration = %v, want zero (scrobble must be skipped)", s.trackDuration)
	}
}

func TestGetTrackDurationValidCustomDuration(t *testing.T) {
	c := &Config{cache: NewInMemory()}
	s := &scrobble{artist: "Artist", track: "Track"}
	userTrackDurations := durationByTrackByArtist{"Artist": {"Track": "3m"}}

	err := getTrackDuration(context.Background(), c, userTrackDurations, s)
	if err != nil {
		t.Fatalf("getTrackDuration unexpected error: %v", err)
	}

	if s.trackDuration != 3*time.Minute {
		t.Errorf("trackDuration = %v, want 3m", s.trackDuration)
	}
}
