// telegram-hub is the homelab's single Telegram app: an MCP server exposing
// send_message and tts_article tools, plus a long-polling bot that converts
// article URLs to audio. Tool recipients are locked server-side via the
// TELEGRAM_CHAT_ID environment variable: clients can never choose another
// chat, so even a prompt-injected task can only message the owner.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	listenAddr = ":8000"

	// mistralTimeout matches the previous Python SDK timeout (300s) to
	// accommodate long articles.
	mistralTimeout = 300 * time.Second
	extractTimeout = 120 * time.Second
	pollTimeout    = 60
)

type sendMessageInput struct {
	Text      string `json:"text" jsonschema:"the text to deliver to the homelab owner's Telegram chat"`
	ParseMode string `json:"parse_mode,omitempty" jsonschema:"optional Telegram formatting: empty (plain text, default), HTML (recommended), or MarkdownV2"`
}

type ttsArticleInput struct {
	URL string `json:"url" jsonschema:"the article URL to convert to a voice message"`
}

func newSendMessageHandler(logger *slog.Logger, client *telegramClient) func(context.Context, *mcp.CallToolRequest, sendMessageInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input sendMessageInput) (*mcp.CallToolResult, any, error) {
		logger.Info("send_message tool called",
			"text_length", len([]rune(input.Text)),
			"parse_mode", input.ParseMode,
		)

		if !validParseMode(input.ParseMode) {
			logger.Warn("rejecting invalid parse_mode", "parse_mode", input.ParseMode)
			return nil, nil, fmt.Errorf("parse_mode must be one of %q, got %q", parseModes, input.ParseMode)
		}

		response, err := client.deliver(ctx, input.Text, input.ParseMode)
		if err != nil {
			logger.Error("delivery failed", "err", err)
			return nil, nil, err
		}

		logger.Info("message delivered", "parse_mode", input.ParseMode)

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: response}},
		}, nil, nil
	}
}

// newTTSArticleHandler converts an article URL to a voice message and posts
// it to the locked owner chat.
func newTTSArticleHandler(logger *slog.Logger, b *bot) func(context.Context, *mcp.CallToolRequest, ttsArticleInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input ttsArticleInput) (*mcp.CallToolResult, any, error) {
		logger.Info("tts_article tool called", "url", input.URL)

		if !isValidURL(input.URL) {
			return nil, nil, fmt.Errorf("url must start with http:// or https://, got %q", input.URL)
		}

		chatID, err := strconv.ParseInt(b.cfg.chatID, 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("TELEGRAM_CHAT_ID not configured: %w", err)
		}

		rawText, err := extractArticleText(ctx, input.URL, b.httpClient)
		if err != nil {
			logger.Error("extracting article", "url", input.URL, "err", err)
			return nil, nil, err
		}

		title := b.mistral.articleTitle(ctx, rawText)

		cacheFile, err := processURL(ctx, b.logger, b.mistral, input.URL, rawText)
		if err != nil {
			logger.Error("generating audio", "url", input.URL, "err", err)
			return nil, nil, err
		}

		duration, err := audioDuration(ctx, cacheFile)
		if err != nil {
			return nil, nil, err
		}

		audio, err := os.ReadFile(cacheFile)
		if err != nil {
			return nil, nil, fmt.Errorf("reading cached audio: %w", err)
		}

		if err := b.sendAudio(chatID, audio, title, duration); err != nil {
			logger.Error("delivering audio", "url", input.URL, "err", err)
			return nil, nil, err
		}

		logger.Info("audio delivered", "url", input.URL, "title", title, "seconds", duration)

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("Audio delivered to the owner's chat: %s (%d seconds)", title, duration),
			}},
		}, nil, nil
	}
}

func runServe(ctx context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := loadConfig()

	if !botEnabled(cfg) {
		logger.Warn("bot disabled: TELEGRAM_BOT_TOKEN or ALLOWED_USERS unset")
	}

	if cfg.botToken == "" || cfg.chatID == "" {
		logger.Warn("send_message and tts_article will fail until TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are set")
	}

	if cfg.mistralAPIKey == "" {
		logger.Warn("tts_article will fail until MISTRAL_API_KEY is set")
	}

	if err := pruneCache(cacheDir, defaultCacheMaxAgeDays); err != nil {
		logger.Warn("pruning cache", "err", err)
	}

	var api *tgbotapi.BotAPI

	if botEnabled(cfg) {
		botAPI, err := tgbotapi.NewBotAPI(cfg.botToken)
		if err != nil {
			return fmt.Errorf("connecting to Telegram: %w", err)
		}

		api = botAPI
	}

	mistral := newMistralClient(&http.Client{Timeout: mistralTimeout}, cfg)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = safeDialContext

	b := &bot{
		api:        api,
		mistral:    mistral,
		httpClient: &http.Client{Timeout: extractTimeout, Transport: transport},
		cfg:        cfg,
		logger:     logger,
	}

	if api != nil {
		updateConfig := tgbotapi.NewUpdate(0)
		updateConfig.Timeout = pollTimeout
		updates := api.GetUpdatesChan(updateConfig)

		go func() {
			logger.Info("bot started", "username", api.Self.UserName)

			for {
				select {
				case <-ctx.Done():
					return
				case update := <-updates:
					go b.handleUpdate(ctx, update)
				}
			}
		}()
	}

	client := newTelegramClient(&http.Client{Timeout: telegramTimeout}, cfg.apiBase, cfg.botToken, cfg.chatID)

	server := mcp.NewServer(&mcp.Implementation{Name: "telegram-hub", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "send_message",
		Description: "Send a message to the homelab owner's Telegram chat. " +
			"parse_mode controls Telegram formatting: \"\" (plain text, default), " +
			"\"HTML\" (recommended: <b>, <i>, <code>, <pre>), or \"MarkdownV2\". " +
			"Escape user content before wrapping it in tags. If Telegram rejects " +
			"the formatted message, it is retried as plain text.",
	}, newSendMessageHandler(logger, client))
	mcp.AddTool(server, &mcp.Tool{
		Name: "tts_article",
		Description: "Convert an article URL to a voice message using Mistral's Voxtral TTS " +
			"and send it to the homelab owner's Telegram chat. " +
			"Repeated URLs are served from a cache without new API calls.",
	}, newTTSArticleHandler(logger, b))

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/mcp", hostGuard(logger, mcpHandler))

	// WriteTimeout is left unset: streamable-HTTP responses can be SSE
	// streams that stay open longer than any fixed write deadline.
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           requestLog(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	logger.Info("listening", "addr", listenAddr)

	select {
	case err := <-errCh:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return srv.Shutdown(shutdownCtx)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}

		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
