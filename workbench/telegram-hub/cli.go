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
)

// errUsage marks invalid command-line usage; main exits 2 for it.
var errUsage = errors.New("usage")

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `telegram-hub — the homelab's Telegram app

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
`)
}

// run dispatches subcommands; bare invocation only prints help.
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage(os.Stdout)
		return nil
	}

	switch args[0] {
	case "serve":
		return runServe(ctx)
	case "tts":
		return runTTS(ctx, args[1:])
	case "send":
		return runSend(ctx, args[1:])
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return errUsage
	}
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

// flagTarget maps a flag name to its destination; nil means unknown flag.
func (f *ttsFlags) flagTarget(name string) *string {
	switch name {
	case "-o", "--output":
		return &f.output
	case "--api-base":
		return &f.apiBase
	case "--clean-model":
		return &f.cleanModel
	case "--title-model":
		return &f.titleModel
	case "--model":
		return &f.model
	case "--voice-en":
		return &f.voiceEn
	case "--voice-fr":
		return &f.voiceFr
	default:
		return nil
	}
}

func parseTTSArgs(args []string) (ttsFlags, error) {
	var flags ttsFlags

	for i := 0; i < len(args); i++ {
		name, value := args[i], ""

		if long, ok := strings.CutPrefix(name, "--"); ok {
			if key, val, found := strings.Cut(long, "="); found {
				name, value = "--"+key, val
			}
		} else if rest, ok := strings.CutPrefix(name, "-o="); ok {
			name, value = "-o", rest
		}

		target := flags.flagTarget(name)
		if target == nil {
			if strings.HasPrefix(name, "-") || flags.url != "" {
				return flags, errUsage
			}

			flags.url = name

			continue
		}

		if value == "" {
			// shortcut: --flag= (empty value) consumes the next argument; drop if it bites
			if i+1 >= len(args) {
				return flags, errUsage
			}

			i++
			value = args[i]
		}

		*target = value
	}

	if flags.url == "" {
		return flags, errUsage
	}

	return flags, nil
}

// runTTS converts one article URL to an MP3 without Telegram: a local test
// of the extract -> clean -> speak pipeline.
func runTTS(ctx context.Context, args []string) error {
	logger := cliLogger()

	flags, err := parseTTSArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: telegram-hub tts <url> [-o <file>] [--model <m>] [--clean-model <m>] [--title-model <m>] [--api-base <url>] [--voice-en <v>] [--voice-fr <v>]")
		return errUsage
	}

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

func parseSendArgs(args []string) (text, parseMode string, err error) {
	var words []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--parse-mode":
			if i+1 >= len(args) {
				return "", "", errUsage
			}

			i++
			parseMode = args[i]
		case strings.HasPrefix(arg, "--parse-mode="):
			parseMode = strings.TrimPrefix(arg, "--parse-mode=")
		case strings.HasPrefix(arg, "-"):
			return "", "", errUsage
		default:
			words = append(words, arg)
		}
	}

	if len(words) == 0 {
		return "", "", errUsage
	}

	return strings.Join(words, " "), parseMode, nil
}

// runSend delivers one message to the locked owner chat from the CLI.
func runSend(ctx context.Context, args []string) error {
	cliLogger()

	text, parseMode, err := parseSendArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, `usage: telegram-hub send <text> [--parse-mode ""|HTML|MarkdownV2]`)
		return errUsage
	}

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
