package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	altsrc "github.com/urfave/cli-altsrc/v3"
	"github.com/urfave/cli-altsrc/v3/yaml"
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
		configPath     string
		inputDir       string
		outputDir      string
		discogsToken   string
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
				Name:        "config",
				Aliases:     []string{"c"},
				Value:       "config.yaml",
				Usage:       "path to the configuration file",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("CONFIG_PATH")),
				Destination: &configPath,
			},
			&cli.StringFlag{
				Name:        "input-dir",
				Aliases:     []string{"i"},
				Required:    true,
				Usage:       "path to the input directory containing music files",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("INPUT_DIR"), yaml.YAML("inputDir", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &inputDir,
			},
			&cli.StringFlag{
				Name:        "output-dir",
				Aliases:     []string{"o"},
				Required:    true,
				Usage:       "path to the output directory where arranged files will be saved",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("OUTPUT_DIR"), yaml.YAML("outputDir", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &outputDir,
			},
			&cli.StringFlag{
				Name:        "jellyfin-url",
				Usage:       "url of the Jellyfin server",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("JELLYFIN_URL"), yaml.YAML("jellyfin.url", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &jellyfinURL,
			},
			&cli.StringFlag{
				Name:        "jellyfin-api-key",
				Usage:       "API key for the Jellyfin server",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("JELLYFIN_API_KEY"), yaml.YAML("jellyfin.apiKey", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &jellyfinAPIKey,
			},
			&cli.StringFlag{
				Name:        "lidarr-url",
				Usage:       "url of the Lidarr server",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LIDARR_URL"), yaml.YAML("lidarr.url", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &lidarrURL,
			},
			&cli.StringFlag{
				Name:        "lidarr-api-key",
				Usage:       "API key for the Lidarr server",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LIDARR_API_KEY"), yaml.YAML("lidarr.apiKey", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &lidarrAPIKey,
			},
			&cli.StringFlag{
				Name:        "discogs-token",
				Usage:       "token for Discogs API",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("DISCOGS_TOKEN"), yaml.YAML("discogs.token", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &discogsToken,
			},
			&cli.StringFlag{
				Name:        "log-level",
				Usage:       "log level",
				Value:       "info",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LOG_LEVEL"), yaml.YAML("logLevel", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &logLevel,
			},
			&cli.BoolFlag{
				Name:        "copy",
				Usage:       "copy instead of moving files",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("COPY"), yaml.YAML("copy", altsrc.NewStringPtrSourcer(&configPath))),
				Destination: &copy,
			},
			&cli.StringFlag{
				Name:        "lock-file",
				Usage:       "path to a lock file; if it exists, the run is skipped",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LOCK_FILE"), yaml.YAML("lockFile", altsrc.NewStringPtrSourcer(&configPath))),
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
				DiscogsToken:   discogsToken,
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
