package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// fixAudioHeader remuxes the MP3 so its header matches the actual stream.
func fixAudioHeader(ctx context.Context, tempPath, outPath string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", tempPath, "-acodec", "copy", outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remuxing audio: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// audioDuration returns the audio file's duration in seconds via ffprobe.
func audioDuration(ctx context.Context, path string) (int, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)

	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("probing audio duration: %w", err)
	}

	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("parsing duration %q: %w", strings.TrimSpace(string(out)), err)
	}

	return int(seconds), nil
}
