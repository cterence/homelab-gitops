package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

func Run(ctx context.Context, c *Config) error {
	err := c.checkConfig()
	if err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	if c.CanDelete {
		slog.Info("⚠️ Scrobble deletion enabled")
	} else {
		slog.Info("Scrobble deletion disabled")
	}

	err = initApp(ctx, c)
	if err != nil {
		return fmt.Errorf("failed to init app: %w", err)
	}

	c.handleInterrupts(ctx)

	err = login(c.taskCtx, c)
	if err != nil {
		return fmt.Errorf("failed to login to Last.fm: %w", err)
	}

	startPage, err := getStartPage(c)
	if err != nil {
		if errors.Is(err, ErrNoScrobbles) {
			slog.Info(ErrNoScrobbles.Error())
			return nil
		}

		return fmt.Errorf("failed to get starting page: %w", err)
	}

	userTrackDurations, err := getUserTrackDurations(c.DataDir)
	if err != nil {
		return fmt.Errorf("failed to get user track durations: %w", err)
	}

	c.unknownTrackDurations = make(durationByTrackByArtist, 0)

	switch c.ProcessingMode {
	case "sequential":
		endPage := 1
		if err := processScrobblesFromStartToEndPage(c.taskCtx, c, startPage, endPage, userTrackDurations); err != nil {
			return fmt.Errorf("error when processing scrobbles: %w", err)
		}
	default:
		return fmt.Errorf("unknown processing mode: %s", c.ProcessingMode)
	}

	slog.Info("Processing complete!")

	if err := finishRun(ctx, c); err != nil {
		return fmt.Errorf("failed to finish run: %w", err)
	}

	slog.Info("Exiting")

	return nil
}
