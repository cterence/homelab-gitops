package main

import "github.com/michiwend/gomusicbrainz"

type Config struct {
	// User input fields
	InputDir       string
	OutputDir      string
	DiscogsToken   string
	Copy           bool
	JellyfinURL    string
	JellyfinAPIKey string
	LidarrURL      string
	LidarrAPIKey   string
	LockFile       string
	// Private dependencies
	mbClient *gomusicbrainz.WS2Client
}
