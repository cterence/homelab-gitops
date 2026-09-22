package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// telegramAPIBase is the public Telegram Bot API endpoint.
	telegramAPIBase = "https://api.telegram.org"

	// Telegram's sendMessage limit is 4096 characters; stay under it.
	maxTextLength = 4000

	// maxResponseLength caps how much of the Telegram response is echoed
	// back to the MCP client.
	maxResponseLength = 200

	telegramTimeout = 10 * time.Second
)

// parseModes are the Telegram formatting modes the tool accepts.
var parseModes = []string{"", "HTML", "MarkdownV2"}

// telegramClient posts messages to a single, server-locked chat.
type telegramClient struct {
	httpClient *http.Client
	apiBase    string
	botToken   string
	chatID     string
	logger     *slog.Logger
}

func newTelegramClient(httpClient *http.Client, apiBase, botToken, chatID string) *telegramClient {
	return &telegramClient{
		httpClient: httpClient,
		apiBase:    apiBase,
		botToken:   botToken,
		chatID:     chatID,
		logger:     slog.Default(),
	}
}

func (c *telegramClient) withLogger(logger *slog.Logger) *telegramClient {
	c.logger = logger
	return c
}

// clampText caps text at maxTextLength runes, so multi-byte characters are
// never split mid-sequence.
func clampText(s string) string {
	if len(s) <= maxTextLength {
		return s
	}

	if r := []rune(s); len(r) > maxTextLength {
		return string(r[:maxTextLength])
	}

	return s
}

func validParseMode(mode string) bool {
	for _, m := range parseModes {
		if m == mode {
			return true
		}
	}

	return false
}

// deliver posts a message to the locked chat, returning a trimmed API
// response. If Telegram rejects the formatting (HTTP 400), it retries once
// as plain text so the message still arrives.
func (c *telegramClient) deliver(ctx context.Context, text, parseMode string) (string, error) {
	text = clampText(text)

	response, err := c.post(ctx, text, parseMode)
	if err != nil {
		if parseMode != "" && isBadRequest(err) {
			c.logger.Info("retrying as plain text after 400", "chat_id", c.chatID)
			return c.post(ctx, text, "")
		}

		return "", err
	}

	return response, nil
}

func (c *telegramClient) post(ctx context.Context, text, parseMode string) (string, error) {
	form := url.Values{}
	form.Set("chat_id", c.chatID)
	form.Set("text", text)

	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", c.apiBase, c.botToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("building sendMessage request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("telegram API unreachable: %w", err)
	}
	// The response body is fully read below; nothing to propagate from Close.
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return "", fmt.Errorf("reading sendMessage response: %w", err)
	}

	trimmed := trimToRunes(string(body), maxResponseLength)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &telegramAPIError{statusCode: resp.StatusCode, body: trimmed}
	}

	return trimmed, nil
}

// telegramAPIError is an HTTP-level rejection by the Telegram API.
type telegramAPIError struct {
	statusCode int
	body       string
}

func (e *telegramAPIError) Error() string {
	return fmt.Sprintf("Telegram API error %d: %s", e.statusCode, e.body)
}

func isBadRequest(err error) bool {
	var apiErr *telegramAPIError
	return errors.As(err, &apiErr) && apiErr.statusCode == http.StatusBadRequest
}

// trimToRunes caps a string at n runes without splitting a multi-byte
// character.
func trimToRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}

	r := []rune(s)
	if len(r) <= n {
		return s
	}

	return string(r[:n])
}
