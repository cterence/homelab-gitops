package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	cli "github.com/urfave/cli/v3"
)

// errUsage marks invalid command-line usage; main exits 2 for it.
var errUsage = errors.New("usage")

const usageText = `telegram-hub — the homelab's Telegram app

Usage:
  telegram-hub serve                            run the MCP server and bot
  telegram-hub tts <url> [flags]                convert an article to MP3; prints
                                                 the cache path unless -o is given
  telegram-hub send <text> [--parse-mode <m>]    message the owner chat

tts flags (override the matching MISTRAL_* environment variables):
  -o, --output <file|->      where to write the MP3
  --model <name>              TTS model
  --clean-model <name>        chat model used to clean the article text
  --title-model <name>        chat model used to extract the title
  --api-base <url>            Mistral API base URL
  --voice-en, --voice-fr <id> voices for English and non-English text

Examples:
  telegram-hub tts https://example.com/article -o article.mp3
  telegram-hub tts https://example.com/article --model voxtral-mini-tts-3-0
  telegram-hub send "hello from the CLI" --parse-mode HTML

Environment: serve needs TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID, MISTRAL_API_KEY
and ALLOWED_USERS. tts needs only MISTRAL_API_KEY. send needs
TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID.
`

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, usageText)
}

// onUsage prints the usage then maps the parse failure onto errUsage.
func onUsage(context.Context, *cli.Command, error, bool) error {
	usage(os.Stderr)

	return errUsage
}

// run dispatches subcommands; bare invocation only prints help.
func run(ctx context.Context, args []string) error {
	cmd := &cli.Command{
		Name:                          "telegram-hub",
		UsageText:                     usageText,
		CustomRootCommandHelpTemplate: "{{.UsageText}}",
		OnUsageError:                  onUsage,
		Action: func(_ context.Context, c *cli.Command) error {
			if c.Args().Present() {
				usage(os.Stderr)

				return errUsage
			}

			usage(os.Stdout)

			return nil
		},
		Commands: []*cli.Command{
			{
				Name:   "serve",
				Usage:  "run the MCP server and bot",
				Action: func(ctx context.Context, _ *cli.Command) error { return runServe(ctx) },
			},
			{
				Name:  "tts",
				Usage: "convert an article to MP3; prints the cache path unless -o is given",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "where to write the MP3"},
					&cli.StringFlag{Name: "model", Usage: "TTS model"},
					&cli.StringFlag{Name: "clean-model", Usage: "chat model used to clean the article text"},
					&cli.StringFlag{Name: "title-model", Usage: "chat model used to extract the title"},
					&cli.StringFlag{Name: "api-base", Usage: "Mistral API base URL"},
					&cli.StringFlag{Name: "voice-en", Usage: "voice for English text"},
					&cli.StringFlag{Name: "voice-fr", Usage: "voice for non-English text"},
				},
				Action: func(ctx context.Context, c *cli.Command) error {
					args := c.Args().Slice()
					if len(args) != 1 {
						fmt.Fprintln(os.Stderr, "usage: telegram-hub tts <url> [-o <file>] [--model <m>] [--clean-model <m>] [--title-model <m>] [--api-base <url>] [--voice-en <v>] [--voice-fr <v>]")

						return errUsage
					}

					return runTTS(ctx, ttsFlags{
						url:        args[0],
						output:     c.String("output"),
						apiBase:    c.String("api-base"),
						cleanModel: c.String("clean-model"),
						titleModel: c.String("title-model"),
						model:      c.String("model"),
						voiceEn:    c.String("voice-en"),
						voiceFr:    c.String("voice-fr"),
					})
				},
			},
			{
				Name:  "send",
				Usage: "message the owner chat",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "parse-mode", Usage: `"" (plain text), HTML, or MarkdownV2`},
				},
				Action: func(ctx context.Context, c *cli.Command) error {
					args := c.Args().Slice()
					if len(args) == 0 {
						fmt.Fprintln(os.Stderr, `usage: telegram-hub send <text> [--parse-mode ""|HTML|MarkdownV2]`)

						return errUsage
					}

					return runSend(ctx, strings.Join(args, " "), c.String("parse-mode"))
				},
			},
		},
	}

	for _, sub := range cmd.Commands {
		sub.OnUsageError = onUsage
	}

	return cmd.Run(ctx, append([]string{"telegram-hub"}, args...))
}

// cliLogger keeps stdout clean for command data; diagnostics go to stderr.
func cliLogger() *slog.Logger {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	return logger
}

// ttsFlags holds the parsed tts command line. Empty override fields fall
// back to the environment (loadConfig), then to the built-in defaults.
type ttsFlags struct {
	url        string
	output     string
	apiBase    string
	cleanModel string
	titleModel string
	model      string
	voiceEn    string
	voiceFr    string
}

// runTTS converts one article URL to an MP3 without Telegram: a local test
// of the extract -> clean -> speak pipeline.
func runTTS(ctx context.Context, flags ttsFlags) error {
	logger := cliLogger()

	if !isValidURL(flags.url) {
		return fmt.Errorf("url must start with http:// or https://, got %q", flags.url)
	}

	// Precedence: flags > environment > defaults.
	cfg := loadConfig()
	cfg.mistralAPIBase = cmp.Or(flags.apiBase, cfg.mistralAPIBase)
	cfg.chatCleanModel = cmp.Or(flags.cleanModel, cfg.chatCleanModel)
	cfg.chatTitleModel = cmp.Or(flags.titleModel, cfg.chatTitleModel)
	cfg.ttsModel = cmp.Or(flags.model, cfg.ttsModel)
	cfg.voiceEnglish = cmp.Or(flags.voiceEn, cfg.voiceEnglish)
	cfg.voiceFrench = cmp.Or(flags.voiceFr, cfg.voiceFrench)

	if cfg.mistralAPIKey == "" {
		return errors.New("MISTRAL_API_KEY environment variable must be set")
	}

	mistral := newMistralClient(&http.Client{Timeout: mistralTimeout}, cfg)

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = safeDialContext

	rawText, err := extractArticleText(ctx, flags.url, &http.Client{Timeout: extractTimeout, Transport: transport})
	if err != nil {
		return fmt.Errorf("extracting article: %w", err)
	}

	cacheFile, err := processURL(ctx, logger, mistral, flags.url, rawText)
	if err != nil {
		return fmt.Errorf("generating audio: %w", err)
	}

	if flags.output == "" {
		logger.Info("audio ready", "url", flags.url, "file", cacheFile)
		return nil
	}

	audio, err := os.ReadFile(cacheFile)
	if err != nil {
		return fmt.Errorf("reading cached audio: %w", err)
	}

	if flags.output == "-" {
		if _, err := os.Stdout.Write(audio); err != nil {
			return fmt.Errorf("writing audio to stdout: %w", err)
		}

		return nil
	}

	if err := os.WriteFile(flags.output, audio, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", flags.output, err)
	}

	logger.Info("audio ready", "url", flags.url, "cache_file", cacheFile, "output", flags.output, "bytes", len(audio))

	return nil
}

// runSend delivers one message to the locked owner chat from the CLI.
func runSend(ctx context.Context, text, parseMode string) error {
	cliLogger()

	if !validParseMode(parseMode) {
		return fmt.Errorf("parse_mode must be one of %q, got %q", parseModes, parseMode)
	}

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")

	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if botToken == "" || chatID == "" {
		return errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID environment variables must be set")
	}

	client := newTelegramClient(&http.Client{Timeout: telegramTimeout}, telegramAPIBase, botToken, chatID)

	response, err := client.deliver(ctx, text, parseMode)
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}

	fmt.Println(response)

	return nil
}
