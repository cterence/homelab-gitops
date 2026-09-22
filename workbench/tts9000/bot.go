package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// bot wires the Telegram API, Mistral client, and cache together.
type bot struct {
	api        *tgbotapi.BotAPI
	mistral    *mistralClient
	httpClient *http.Client
	cfg        config
	logger     *slog.Logger
}

// isAllowedUser reports whether the user may use the bot; an empty allow
// list allows everyone.
func isAllowedUser(allowed []string, userID int64) bool {
	if len(allowed) == 0 {
		return true
	}

	id := strconv.FormatInt(userID, 10)
	for _, u := range allowed {
		if u == id {
			return true
		}
	}

	return false
}

// isValidURL reports whether the text starts with an http(s) scheme.
func isValidURL(text string) bool {
	return strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://")
}

func (b *bot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	if update.Message == nil {
		return
	}

	if update.Message.IsCommand() {
		if update.Message.Command() == "start" {
			b.logger.Info("start command", "user_id", update.Message.From.ID)
			b.reply(update.Message, "Send me a URL and I'll convert the article to audio!")
		}

		return
	}

	if update.Message.Text == "" {
		return
	}

	b.handleURL(ctx, update.Message)
}

func (b *bot) handleURL(ctx context.Context, msg *tgbotapi.Message) {
	userID := msg.From.ID
	if !isAllowedUser(b.cfg.allowedUsers, userID) {
		b.logger.Warn("unauthorized access attempt", "user_id", userID)
		b.reply(msg, "You are not authorized to use this bot.")

		return
	}

	pageURL := msg.Text
	if !isValidURL(pageURL) {
		b.logger.Info("rejecting non-URL message", "user_id", userID)
		b.reply(msg, "Please send a valid URL starting with http:// or https://")

		return
	}

	b.logger.Info("received URL", "user_id", userID, "url", pageURL)

	if err := b.processArticle(ctx, msg, pageURL); err != nil {
		b.logger.Error("processing URL", "url", pageURL, "err", err)
		b.reply(msg, friendlyError(err))
	}
}

func (b *bot) processArticle(ctx context.Context, msg *tgbotapi.Message, pageURL string) error {
	progress := b.reply(msg, "Extracting text from webpage...")

	rawText, err := extractArticleText(ctx, pageURL, b.httpClient)
	if err != nil {
		return err
	}

	b.logger.Info("text extracted", "url", pageURL, "chars", len([]rune(rawText)))

	articleTitle := b.mistral.articleTitle(ctx, rawText)
	b.logger.Info("article title extracted", "url", pageURL, "title", articleTitle)
	b.edit(progress, fmt.Sprintf("Cleaning text for %s...", articleTitle))

	audioData, err := b.processURL(ctx, pageURL, rawText)
	if err != nil {
		return err
	}

	b.edit(progress, "Generating TTS...")

	cacheFile := getCacheFilename(pageURL)

	tempFile := cacheFile + ".temp"
	if err := os.WriteFile(tempFile, audioData, 0o600); err != nil {
		return fmt.Errorf("writing temp audio: %w", err)
	}

	b.edit(progress, "Fixing audio header...")

	if err := fixAudioHeader(ctx, tempFile, cacheFile); err != nil {
		_ = os.Remove(tempFile)
		return err
	}

	duration, err := audioDuration(ctx, cacheFile)
	if err != nil {
		_ = os.Remove(tempFile)
		return err
	}

	b.logger.Info("audio remuxed", "url", pageURL, "file", cacheFile, "bytes", len(audioData), "seconds", duration)

	if err := os.Remove(tempFile); err != nil {
		b.logger.Warn("removing temp audio", "err", err)
	}

	b.delete(progress)

	audio, err := os.ReadFile(cacheFile)
	if err != nil {
		return fmt.Errorf("reading cached audio: %w", err)
	}

	b.replyAudio(msg, audio, articleTitle, duration)
	b.logger.Info("audio delivered", "url", pageURL, "chat_id", msg.Chat.ID, "title", articleTitle, "seconds", duration)

	return nil
}

// processURL returns the audio for a URL, using the cache when available
// and generating (then caching) otherwise.
func (b *bot) processURL(ctx context.Context, pageURL, rawText string) ([]byte, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	cacheFile := getCacheFilename(pageURL)
	if data, err := os.ReadFile(cacheFile); err == nil {
		b.logger.Info("cache hit", "url", pageURL, "file", cacheFile)
		return data, nil
	}

	b.logger.Info("cache miss, generating audio", "url", pageURL)

	cleanText, err := b.mistral.cleanText(ctx, rawText)
	if err != nil {
		return nil, fmt.Errorf("cleaning text: %w", err)
	}

	b.logger.Info("text cleaned", "url", pageURL, "chars", len([]rune(cleanText)))

	audio, err := b.mistral.generateTTS(ctx, cleanText)
	if err != nil {
		return nil, fmt.Errorf("generating TTS: %w", err)
	}

	b.logger.Info("tts generated", "url", pageURL, "bytes", len(audio))

	if err := os.WriteFile(cacheFile, audio, 0o600); err != nil {
		return nil, fmt.Errorf("writing cache file %s: %w", cacheFile, err)
	}

	return audio, nil
}

// friendlyError maps processing errors to user-facing replies.
func friendlyError(err error) string {
	if errors.Is(err, errBlocked) || strings.Contains(err.Error(), "403") {
		return "Access blocked. This site may have Cloudflare or similar protection that prevents automated access."
	}

	return err.Error()
}

func (b *bot) reply(msg *tgbotapi.Message, text string) *tgbotapi.Message {
	sent, err := b.api.Send(tgbotapi.NewMessage(msg.Chat.ID, text))
	if err != nil {
		b.logger.Error("sending reply", "err", err)
		return nil
	}

	return &sent
}

func (b *bot) edit(msg *tgbotapi.Message, text string) {
	if msg == nil {
		return
	}

	edit := tgbotapi.NewEditMessageText(msg.Chat.ID, msg.MessageID, text)
	if _, err := b.api.Request(edit); err != nil {
		b.logger.Error("editing message", "err", err)
	}
}

func (b *bot) delete(msg *tgbotapi.Message) {
	if msg == nil {
		return
	}

	if _, err := b.api.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, msg.MessageID)); err != nil {
		b.logger.Error("deleting message", "err", err)
	}
}

func (b *bot) replyAudio(msg *tgbotapi.Message, audio []byte, title string, duration int) {
	audioCfg := tgbotapi.AudioConfig{
		BaseFile: tgbotapi.BaseFile{
			BaseChat: tgbotapi.BaseChat{ChatID: msg.Chat.ID},
			File:     tgbotapi.FileBytes{Bytes: audio, Name: "article.mp3"},
		},
		Title:    title,
		Duration: duration,
	}
	if _, err := b.api.Send(audioCfg); err != nil {
		b.logger.Error("sending audio", "err", err)
	}
}
