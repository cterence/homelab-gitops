package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cterence/homelab-gitops/image-builds/ig-telegram/instagram"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.temporal.io/sdk/client"
)

// Bot polls Telegram for updates and starts Temporal workflows in response
// to Instagram links or /profile commands.
type Bot struct {
	api       *tgbotapi.BotAPI
	temporal  client.Client
	sender    *Sender
	allowed   int64
	taskQueue string
	logger    *slog.Logger
}

func NewBot(token string, temporal client.Client, allowedUserID int64, taskQueue string, logger *slog.Logger) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("creating telegram bot api: %w", err)
	}
	return &Bot{
		api:       api,
		temporal:  temporal,
		sender:    &Sender{api: api},
		allowed:   allowedUserID,
		taskQueue: taskQueue,
		logger:    logger,
	}, nil
}

// Start begins polling for updates until ctx is canceled.
func (b *Bot) Start(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := b.api.GetUpdatesChan(u)
	b.logger.Info("telegram bot started", "username", b.api.Self.UserName)

	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return nil
		case update := <-updates:
			if update.Message == nil {
				continue
			}
			b.handleUpdate(ctx, update)
		}
	}
}

func (b *Bot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	msg := update.Message

	// Auth guard: reject messages from unauthorized users.
	if msg.From == nil || msg.From.ID != b.allowed {
		b.logger.Warn("rejected message from unauthorized user",
			"user_id", msg.From.ID)
		return
	}

	// Handle /profile command.
	if strings.HasPrefix(msg.Text, "/profile") {
		b.handleProfileCommand(ctx, msg)
		return
	}

	// Handle raw Instagram post/reel links.
	if instagram.IsPostURL(msg.Text) {
		b.handlePostLink(ctx, msg)
		return
	}

	// Unknown input.
	if err := b.sender.Reply(ctx, msg.Chat.ID, msg.MessageID,
		"Send me an Instagram post/reel link, or use /profile <username> [limit] [reels]"); err != nil {
		b.logger.Warn("failed to send reply", "err", err)
	}
}

func (b *Bot) handlePostLink(ctx context.Context, msg *tgbotapi.Message) {
	postURL := strings.TrimSpace(msg.Text)
	b.logger.Info("starting single post workflow",
		"chat_id", msg.Chat.ID, "url", postURL)

	if err := b.sender.SendChatAction(ctx, msg.Chat.ID, tgbotapi.ChatTyping); err != nil {
		b.logger.Warn("failed to send chat action", "err", err)
	}

	workflowID := fmt.Sprintf("ig-post-%d-%d", msg.Chat.ID, time.Now().Unix())
	run, err := b.temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: b.taskQueue,
	}, "SinglePostWorkflow", msg.Chat.ID, postURL)
	if err != nil {
		b.logger.Error("failed to start workflow", "err", err)
		if err := b.sender.Reply(ctx, msg.Chat.ID, msg.MessageID,
			"Failed to start download: "+SanitizeErrorMessage(err.Error())); err != nil {
			b.logger.Warn("failed to send reply", "err", err)
		}
		return
	}

	if err := b.sender.Reply(ctx, msg.Chat.ID, msg.MessageID,
		fmt.Sprintf("Downloading %s ...", postURL)); err != nil {
		b.logger.Warn("failed to send reply", "err", err)
	}

	// Wait for the workflow in a goroutine. Use a detached context so the
	// result polling survives the bot's shutdown signal — the goroutine
	// exits naturally when run.Get returns.
	go func() {
		bgCtx := context.WithoutCancel(ctx)
		var result string
		if err := run.Get(bgCtx, &result); err != nil {
			b.logger.Error("workflow failed", "id", workflowID, "err", err)
			if err := b.sender.SendMessage(bgCtx, msg.Chat.ID,
				"Download failed: "+SanitizeErrorMessage(err.Error())); err != nil {
				b.logger.Warn("failed to send error message", "err", err)
			}
			return
		}
		b.logger.Info("workflow completed", "id", workflowID, "result", result)
	}()
}

func (b *Bot) handleProfileCommand(ctx context.Context, msg *tgbotapi.Message) {
	username, limit, onlyReels, err := ParseProfileCommand(msg.Text)
	if err != nil {
		if err := b.sender.Reply(ctx, msg.Chat.ID, msg.MessageID, err.Error()); err != nil {
			b.logger.Warn("failed to send reply", "err", err)
		}
		return
	}

	profileURL := instagram.ProfileURL(username)
	b.logger.Info("starting profile batch workflow",
		"chat_id", msg.Chat.ID, "profile", profileURL,
		"limit", limit, "only_reels", onlyReels)

	if err := b.sender.SendChatAction(ctx, msg.Chat.ID, tgbotapi.ChatTyping); err != nil {
		b.logger.Warn("failed to send chat action", "err", err)
	}

	workflowID := fmt.Sprintf("ig-profile-%d-%d", msg.Chat.ID, time.Now().Unix())
	run, err := b.temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: b.taskQueue,
	}, "ProfileBatchWorkflow", msg.Chat.ID, profileURL, limit, onlyReels)
	if err != nil {
		b.logger.Error("failed to start workflow", "err", err)
		if err := b.sender.Reply(ctx, msg.Chat.ID, msg.MessageID,
			"Failed to start profile download: "+SanitizeErrorMessage(err.Error())); err != nil {
			b.logger.Warn("failed to send reply", "err", err)
		}
		return
	}

	if err := b.sender.Reply(ctx, msg.Chat.ID, msg.MessageID,
		fmt.Sprintf("Fetching up to %d posts from @%s ...", limit, username)); err != nil {
		b.logger.Warn("failed to send reply", "err", err)
	}

	go func() {
		bgCtx := context.WithoutCancel(ctx)
		var result string
		if err := run.Get(bgCtx, &result); err != nil {
			b.logger.Error("profile workflow failed", "id", workflowID, "err", err)
			if err := b.sender.SendMessage(bgCtx, msg.Chat.ID,
				"Profile download failed: "+SanitizeErrorMessage(err.Error())); err != nil {
				b.logger.Warn("failed to send error message", "err", err)
			}
			return
		}
		b.logger.Info("profile workflow completed", "id", workflowID, "result", result)
	}()
}
