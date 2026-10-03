package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"
)

func setLogger(logLevel string) error {
	var slogLogLevel slog.Level

	switch logLevel {
	case "debug":
		slogLogLevel = slog.LevelDebug
	case "info":
		slogLogLevel = slog.LevelInfo
	case "warn":
		slogLogLevel = slog.LevelWarn
	case "error":
		slogLogLevel = slog.LevelError
	default:
		return fmt.Errorf("unknown log level: %s", logLevel)
	}

	logOpts := slog.HandlerOptions{
		Level: slogLogLevel,
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &logOpts)))

	return nil
}

func main() {
	var (
		version = "dev"
		commit  = "unknown"
		date    = "unknown"
	)

	var (
		inputDir       string
		outputDir      string
		copy           bool
		logLevel       string
		jellyfinURL    string
		jellyfinAPIKey string
		lidarrURL      string
		lidarrAPIKey   string
		lockFile       string
	)

	cmd := &cli.Command{
		Name:    "rangemusique",
		Usage:   "Arrange music files based on metadata",
		Version: fmt.Sprintf("Version: %s\nCommit: %s\nBuild Date: %s", version, commit, date),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "input-dir",
				Aliases:     []string{"i"},
				Required:    true,
				Usage:       "path to the input directory containing music files",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("INPUT_DIR")),
				Destination: &inputDir,
			},
			&cli.StringFlag{
				Name:        "output-dir",
				Aliases:     []string{"o"},
				Required:    true,
				Usage:       "path to the output directory where arranged files will be saved",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("OUTPUT_DIR")),
				Destination: &outputDir,
			},
			&cli.StringFlag{
				Name:        "jellyfin-url",
				Usage:       "url of the Jellyfin server",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("JELLYFIN_URL")),
				Destination: &jellyfinURL,
			},
			&cli.StringFlag{
				Name:        "jellyfin-api-key",
				Usage:       "API key for the Jellyfin server",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("JELLYFIN_API_KEY")),
				Destination: &jellyfinAPIKey,
			},
			&cli.StringFlag{
				Name:        "lidarr-url",
				Usage:       "url of the Lidarr server",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LIDARR_URL")),
				Destination: &lidarrURL,
			},
			&cli.StringFlag{
				Name:        "lidarr-api-key",
				Usage:       "API key for the Lidarr server",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LIDARR_API_KEY")),
				Destination: &lidarrAPIKey,
			},
			&cli.StringFlag{
				Name:        "log-level",
				Usage:       "log level",
				Value:       "info",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LOG_LEVEL")),
				Destination: &logLevel,
			},
			&cli.BoolFlag{
				Name:        "copy",
				Usage:       "copy instead of moving files",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("COPY")),
				Destination: &copy,
			},
			&cli.StringFlag{
				Name:        "lock-file",
				Usage:       "path to a lock file; if it exists, the run is skipped",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LOCK_FILE")),
				Destination: &lockFile,
			},
		},
		Action: func(context.Context, *cli.Command) error {
			ctx := context.Background()

			err := setLogger(logLevel)
			if err != nil {
				return fmt.Errorf("failed to set logger: %w", err)
			}

			cfg := Config{
				InputDir:       inputDir,
				OutputDir:      outputDir,
				Copy:           copy,
				JellyfinURL:    jellyfinURL,
				JellyfinAPIKey: jellyfinAPIKey,
				LidarrURL:      lidarrURL,
				LidarrAPIKey:   lidarrAPIKey,
				LockFile:       lockFile,
			}

			return Run(ctx, cfg)
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}
