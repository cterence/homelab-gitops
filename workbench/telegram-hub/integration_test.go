package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestTTSHeaderDuration compares the ffprobe-reported duration of raw Mistral
// TTS output with the same audio after the ffmpeg remux, to check whether
// fixAudioHeader is still needed. Gated: needs MISTRAL_API_KEY plus ffmpeg
// and ffprobe on PATH.
func TestTTSHeaderDuration(t *testing.T) {
	// CI runs the gate flag without homelab credentials; skip, don't fail.
	if os.Getenv("TELEGRAM_HUB_INTEGRATION") != "1" || os.Getenv("MISTRAL_API_KEY") == "" {
		t.Skip("set TELEGRAM_HUB_INTEGRATION=1 and MISTRAL_API_KEY (requires ffmpeg)")
	}

	mistral := newMistralClient(&http.Client{Timeout: mistralTimeout}, loadConfig())

	ctx, cancel := context.WithTimeout(t.Context(), mistralTimeout)
	defer cancel()

	text := "This is a duration probe for the text to speech pipeline. " +
		"It contains several sentences so the audio runs long enough for a " +
		"meaningful measurement. The quick brown fox jumps over the lazy dog, " +
		"and the rain in Spain stays mainly in the plain."

	audio, err := mistral.generateTTS(ctx, text)
	if err != nil {
		t.Fatalf("generateTTS() error = %v", err)
	}

	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.mp3")
	remuxedPath := filepath.Join(dir, "remuxed.mp3")

	if err := os.WriteFile(rawPath, audio, 0o600); err != nil {
		t.Fatalf("writing raw audio: %v", err)
	}

	rawDuration, err := audioDuration(ctx, rawPath)
	if err != nil {
		t.Fatalf("probing raw audio: %v", err)
	}

	if err := fixAudioHeader(ctx, rawPath, remuxedPath); err != nil {
		t.Fatalf("remuxing audio: %v", err)
	}

	remuxedDuration, err := audioDuration(ctx, remuxedPath)
	if err != nil {
		t.Fatalf("probing remuxed audio: %v", err)
	}

	remuxedSize := len(audio)
	if info, err := os.Stat(remuxedPath); err == nil {
		remuxedSize = int(info.Size())
	}

	t.Logf("raw duration: %d (%.3fs), remuxed duration: %d (%.3fs), raw bytes: %d, remuxed bytes: %d",
		rawDuration, probeSeconds(t, ctx, rawPath), remuxedDuration, probeSeconds(t, ctx, remuxedPath), len(audio), remuxedSize)
}

// probeSeconds reports the ffprobe duration with full precision, unlike the
// int-truncating audioDuration used by the delivery path.
func probeSeconds(t *testing.T, ctx context.Context, path string) float64 {
	t.Helper()

	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("probing %s: %v", path, err)
	}

	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("parsing duration %q: %v", strings.TrimSpace(string(out)), err)
	}

	return seconds
}
