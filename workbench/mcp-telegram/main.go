// mcp-telegram is a minimal Telegram notification MCP server. It exposes a
// single send_message tool that delivers text to the homelab owner's Telegram
// chat. The recipient is locked server-side via the TELEGRAM_CHAT_ID
// environment variable: clients can never choose another chat, so even a
// prompt-injected task can only message the owner.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const listenAddr = ":8000"

// config is the server configuration, read from the environment at startup.
type config struct {
	botToken string
	chatID   string
	// apiBase defaults to the public Telegram API; TELEGRAM_API_BASE can
	// point it at a local fake for end-to-end testing.
	apiBase string
}

func loadConfig() (config, error) {
	cfg := config{
		botToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		chatID:   os.Getenv("TELEGRAM_CHAT_ID"),
		apiBase:  telegramAPIBase,
	}
	if override := os.Getenv("TELEGRAM_API_BASE"); override != "" {
		cfg.apiBase = override
	}

	if cfg.botToken == "" || cfg.chatID == "" {
		return cfg, errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID environment variables must be set")
	}

	return cfg, nil
}

type sendMessageInput struct {
	Text      string `json:"text" jsonschema:"the text to deliver to the homelab owner's Telegram chat"`
	ParseMode string `json:"parse_mode,omitempty" jsonschema:"optional Telegram formatting: empty (plain text, default), HTML (recommended), or MarkdownV2"`
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

func run(ctx context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	client := newTelegramClient(&http.Client{Timeout: telegramTimeout}, cfg.apiBase, cfg.botToken, cfg.chatID)

	server := mcp.NewServer(&mcp.Implementation{Name: "telegram-notify", Version: "0.3.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "send_message",
		Description: "Send a message to the homelab owner's Telegram chat. " +
			"parse_mode controls Telegram formatting: \"\" (plain text, default), " +
			"\"HTML\" (recommended: <b>, <i>, <code>, <pre>), or \"MarkdownV2\". " +
			"Escape user content before wrapping it in tags. If Telegram rejects " +
			"the formatted message, it is retried as plain text.",
	}, newSendMessageHandler(logger, client))

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

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
