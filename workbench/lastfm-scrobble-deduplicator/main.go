package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"time"

	altsrc "github.com/urfave/cli-altsrc/v3"
	"github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
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
		configFilePath     string
		cacheType          string
		lastFMUsername     string
		lastFMPassword     string
		startPage          int
		fromStr            string
		toStr              string
		browserHeadful     bool
		browserURL         string
		redisURL           string
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
		Version: fmt.Sprintf("Version: %s\nCommit: %s\nBuild Date: %s", version, commit, date),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Aliases:     []string{"c"},
				Value:       "config.yaml",
				Usage:       "Path to the configuration file",
				Destination: &configFilePath,
			},
			&cli.StringFlag{
				Name:        "lastfm-username",
				Aliases:     []string{"u"},
				Usage:       "Last.fm username",
				Required:    true,
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LASTFM_USERNAME"), yaml.YAML("lastfm.username", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &lastFMUsername,
			},
			&cli.StringFlag{
				Name:        "lastfm-password",
				Aliases:     []string{"p"},
				Usage:       "Last.fm password",
				Required:    true,
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LASTFM_PASSWORD"), yaml.YAML("lastfm.password", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &lastFMPassword,
			},
			&cli.BoolFlag{
				Name:        "delete",
				Usage:       "Delete duplicate scrobbles",
				Value:       false,
				Sources:     cli.NewValueSourceChain(cli.EnvVar("DELETE"), yaml.YAML("delete", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &canDelete,
			},
			&cli.IntFlag{
				Name:        "duplicate-threshold",
				Usage:       "Percentage of a track's duration below which two successive scrobbles are considered duplicates",
				Value:       90,
				Sources:     cli.NewValueSourceChain(cli.EnvVar("DUPLICATE_THRESHOLD"), yaml.YAML("duplicateThreshold", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &duplicateThreshold,
			},
			&cli.IntFlag{
				Name:        "complete-threshold",
				Usage:       "Percentage of a track's duration to consider a scrobble complete, set a value to enable",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("COMPLETE_THRESHOLD"), yaml.YAML("completeThreshold", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &completeThreshold,
			},
			&cli.IntFlag{
				Name:        "start-page",
				Aliases:     []string{"s"},
				Usage:       "Last.fm scrobble library page to start from",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("START_PAGE"), yaml.YAML("startPage", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &startPage,
			},
			&cli.StringFlag{
				Name:        "from",
				Usage:       "Day at which the program should start deduplicating scrobbles (dd-mm-yyyy, or \"yesterday\"/\"today\")",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("FROM"), yaml.YAML("from", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &fromStr,
			},
			&cli.StringFlag{
				Name:        "to",
				Usage:       "Day at which the program should end deduplicating scrobbles (dd-mm-yyyy, or \"yesterday\"/\"today\")",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("TO"), yaml.YAML("to", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &toStr,
			},
			&cli.StringFlag{
				Name:        "cache-type",
				Usage:       "Cache type for MusicBrainz API queries (inmemory, file, redis) (must specify redis-url flag for redis)",
				Value:       "inmemory",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("CACHE_TYPE"), yaml.YAML("cacheType", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &cacheType,
			},
			&cli.BoolFlag{
				Name:        "browser-headful",
				Usage:       "Run with a visible browser UI",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("BROWSER_HEADFUL"), yaml.YAML("browserHeadful", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &browserHeadful,
			},
			&cli.StringFlag{
				Name:        "browser-url",
				Usage:       "Remote browser URL",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("BROWSER_URL"), yaml.YAML("browserURL", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &browserURL,
			},
			&cli.StringFlag{
				Name:        "redis-url",
				Usage:       "Redis URL for redis cache type",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("REDIS_URL"), yaml.YAML("redisURL", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &redisURL,
			},
			&cli.StringFlag{
				Name:        "data-dir",
				Usage:       "Path to a directory that this program can use to read and produce files",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("DATA_DIR"), yaml.YAML("dataDir", altsrc.NewStringPtrSourcer(&configFilePath))),
				Value:       path.Join(wd, "data"),
				Destination: &dataDir,
			},
			&cli.StringFlag{
				Name:        "log-level",
				Usage:       "Log level (debug, info, warn, error)",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("LOG_LEVEL"), yaml.YAML("logLevel", altsrc.NewStringPtrSourcer(&configFilePath))),
				Value:       "info",
				Destination: &logLevel,
			},
			&cli.StringFlag{
				Name:        "telegram-bot-token",
				Usage:       "Telegram Bot token to send a message to when a run finishes",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("TELEGRAM_BOT_TOKEN"), yaml.YAML("telegram.botToken", altsrc.NewStringPtrSourcer(&configFilePath))),
				Destination: &telegramBotToken,
			},
			&cli.StringFlag{
				Name:        "telegram-chat-id",
				Usage:       "Telegram chat ID where the bot can send message to",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("TELEGRAM_CHAT_ID"), yaml.YAML("telegram.chatID", altsrc.NewStringPtrSourcer(&configFilePath))),
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
				FilePath:           configFilePath,
				CacheType:          cacheType,
				LastFMUsername:     lastFMUsername,
				LastFMPassword:     lastFMPassword,
				StartPage:          startPage,
				From:               from,
				To:                 to,
				BrowserHeadful:     browserHeadful,
				RedisURL:           redisURL,
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
