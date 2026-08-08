// Package telegram provides the Telegram bot integration: a sender for
// dispatching media and messages, and a bot that polls for updates and
// triggers Temporal workflows.
package telegram

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Sender wraps the Telegram Bot API for sending media and messages.
// It is safe for concurrent use.
type Sender struct {
	api *tgbotapi.BotAPI
}

func NewSender(token string) (*Sender, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("creating telegram bot api: %w", err)
	}
	return &Sender{api: api}, nil
}

// SendMedia sends a photo or video file to the given chat. The local file
// is deleted after sending (or on error).
func (s *Sender) SendMedia(ctx context.Context, chatID int64, localPath string, isVideo bool) error {
	defer os.Remove(localPath)

	if isVideo {
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(localPath))
		if _, err := s.api.Send(video); err != nil {
			return fmt.Errorf("sending video: %w", err)
		}
		return nil
	}

	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(localPath))
	if _, err := s.api.Send(photo); err != nil {
		return fmt.Errorf("sending photo: %w", err)
	}
	return nil
}

// SendMessage sends a text message to the given chat.
func (s *Sender) SendMessage(ctx context.Context, chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := s.api.Send(msg); err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	return nil
}

// SendChatAction sends a chat action (e.g. typing) to indicate progress.
func (s *Sender) SendChatAction(ctx context.Context, chatID int64, action string) error {
	chataction := tgbotapi.NewChatAction(chatID, action)
	if _, err := s.api.Request(chataction); err != nil {
		return fmt.Errorf("sending chat action: %w", err)
	}
	return nil
}

// SendMediaGroup sends multiple photos as an album to the given chat.
func (s *Sender) SendMediaGroup(ctx context.Context, chatID int64, paths []string) error {
	media := make([]any, 0, len(paths))
	for _, p := range paths {
		media = append(media, tgbotapi.NewInputMediaPhoto(tgbotapi.FilePath(p)))
	}
	mg := tgbotapi.NewMediaGroup(chatID, media)
	if _, err := s.api.SendMediaGroup(mg); err != nil {
		return fmt.Errorf("sending media group: %w", err)
	}
	return nil
}

// Reply sends a text reply to the original message.
func (s *Sender) Reply(ctx context.Context, chatID int64, messageID int, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = messageID
	if _, err := s.api.Send(msg); err != nil {
		return fmt.Errorf("sending reply: %w", err)
	}
	return nil
}

// SanitizeErrorMessage removes file paths and internal details from errors
// before sending them to the user.
func SanitizeErrorMessage(err string) string {
	// Truncate long errors and strip potential file paths.
	if len(err) > 200 {
		err = err[:200] + "..."
	}
	return err
}

// ParseProfileCommand parses "/profile <username> [limit] [reels]" and
// returns the username, limit, and onlyReels flag.
func ParseProfileCommand(text string) (username string, limit int, onlyReels bool, err error) {
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return "", 0, false, fmt.Errorf("usage: /profile <username> [limit] [reels]")
	}
	username = strings.TrimPrefix(parts[1], "@")
	limit = 12
	onlyReels = false

	if len(parts) >= 3 {
		limit, err = strconv.Atoi(parts[2])
		if err != nil {
			return "", 0, false, fmt.Errorf("invalid limit: %s", parts[2])
		}
	}
	if len(parts) >= 4 {
		onlyReels = strings.EqualFold(parts[3], "reels")
	}
	return username, limit, onlyReels, nil
}
