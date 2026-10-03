package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"time"

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
		cacheType          string
		lastFMUsername     string
		lastFMPassword     string
		startPage          int
		fromStr            string
		toStr              string
		browserHeadful     bool
		browserURL         string
		canDelete          bool
		logLevel           string
		duplicateThreshold int
		completeThreshold  int
		dataDir            string
		telegramBotToken   string
		telegramChatID     string
	)

	wd, err := os.Getwd()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}

	cmd := &cli.Command{
		Name:    "lastfm-scrobble-deduplicator",
		Usage:   "Deduplicate Last.fm scrobbles",
		Version: "dev",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "lastfm-username",
				Aliases:     []string{"u"},
				Usage:       "Last.fm username",
				Required:    true,
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LASTFM_USERNAME")),
				Destination: &lastFMUsername,
			},
			&cli.StringFlag{
				Name:        "lastfm-password",
				Aliases:     []string{"p"},
				Usage:       "Last.fm password",
				Required:    true,
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LASTFM_PASSWORD")),
				Destination: &lastFMPassword,
			},
			&cli.BoolFlag{
				Name:        "delete",
				Usage:       "Delete duplicate scrobbles",
				Value:       false,
				Sources:     cli.NewValueSourceChain(cli.EnvVar("DELETE")),
				Destination: &canDelete,
			},
			&cli.IntFlag{
				Name:        "duplicate-threshold",
				Usage:       "Percentage of a track's duration below which two successive scrobbles are considered duplicates",
				Value:       90,
				Sources:     cli.NewValueSourceChain(cli.EnvVar("DUPLICATE_THRESHOLD")),
				Destination: &duplicateThreshold,
			},
			&cli.IntFlag{
				Name:        "complete-threshold",
				Usage:       "Percentage of a track's duration to consider a scrobble complete, set a value to enable",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("COMPLETE_THRESHOLD")),
				Destination: &completeThreshold,
			},
			&cli.IntFlag{
				Name:        "start-page",
				Aliases:     []string{"s"},
				Usage:       "Last.fm scrobble library page to start from",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("START_PAGE")),
				Destination: &startPage,
			},
			&cli.StringFlag{
				Name:        "from",
				Usage:       "Day at which the program should start deduplicating scrobbles (dd-mm-yyyy, or \"yesterday\"/\"today\")",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("FROM")),
				Destination: &fromStr,
			},
			&cli.StringFlag{
				Name:        "to",
				Usage:       "Day at which the program should end deduplicating scrobbles (dd-mm-yyyy, or \"yesterday\"/\"today\")",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("TO")),
				Destination: &toStr,
			},
			&cli.StringFlag{
				Name:        "cache-type",
				Usage:       "Cache type for MusicBrainz API queries (inmemory, file)",
				Value:       "inmemory",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("CACHE_TYPE")),
				Destination: &cacheType,
			},
			&cli.BoolFlag{
				Name:        "browser-headful",
				Usage:       "Run with a visible browser UI",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("BROWSER_HEADFUL")),
				Destination: &browserHeadful,
			},
			&cli.StringFlag{
				Name:        "browser-url",
				Usage:       "Remote browser URL",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("BROWSER_URL")),
				Destination: &browserURL,
			},
			&cli.StringFlag{
				Name:        "data-dir",
				Usage:       "Path to a directory that this program can use to read and produce files",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("DATA_DIR")),
				Value:       path.Join(wd, "data"),
				Destination: &dataDir,
			},
			&cli.StringFlag{
				Name:        "log-level",
				Usage:       "Log level (debug, info, warn, error)",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LOG_LEVEL")),
				Value:       "info",
				Destination: &logLevel,
			},
			&cli.StringFlag{
				Name:        "telegram-bot-token",
				Usage:       "Telegram Bot token to send a message to when a run finishes",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("TELEGRAM_BOT_TOKEN")),
				Destination: &telegramBotToken,
			},
			&cli.StringFlag{
				Name:        "telegram-chat-id",
				Usage:       "Telegram chat ID where the bot can send message to",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("TELEGRAM_CHAT_ID")),
				Destination: &telegramChatID,
			},
		},
		Action: func(context.Context, *cli.Command) error {
			ctx := context.Background()

			from, err := parseDay(fromStr, time.Now())
			if err != nil {
				return fmt.Errorf("invalid from day: %w", err)
			}

			to, err := parseDay(toStr, time.Now())
			if err != nil {
				return fmt.Errorf("invalid to day: %w", err)
			}

			c := Config{
				CacheType:          cacheType,
				LastFMUsername:     lastFMUsername,
				LastFMPassword:     lastFMPassword,
				StartPage:          startPage,
				From:               from,
				To:                 to,
				BrowserHeadful:     browserHeadful,
				BrowserURL:         browserURL,
				CanDelete:          canDelete,
				LogLevel:           logLevel,
				DuplicateThreshold: duplicateThreshold,
				CompleteThreshold:  completeThreshold,
				DataDir:            dataDir,
				TelegramBotToken:   telegramBotToken,
				TelegramChatID:     telegramChatID,
			}

			err = setLogger(c.LogLevel)
			if err != nil {
				return fmt.Errorf("failed to set logger: %w", err)
			}

			return Run(ctx, &c)
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}
