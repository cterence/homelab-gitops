package main

import (
	"fmt"

	"github.com/michiwend/gomusicbrainz"
)

func initApp(cfg *Config) error {
	mb, err := gomusicbrainz.NewWS2Client("https://musicbrainz.org", "rangemusique", "1.0", "https://github.com/cterence")
	if err != nil {
		return fmt.Errorf("failed to create MusicBrainz client: %w", err)
	}

	cfg.mbClient = mb

	return nil
}
