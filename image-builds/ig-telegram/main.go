// ig-telegram is a Telegram bot that downloads public Instagram media
// (posts, reels, profile grids) using go-rod for browser automation and
// Temporal for workflow orchestration and retries.
//
// It runs as a single binary that hosts the Temporal worker (which executes
// activities) and either the Telegram bot (default) or a one-shot CLI that
// triggers a workflow and waits for the result.
//
// Usage:
//
//	ig-telegram                         # run worker + telegram bot (default)
//	ig-telegram bot                      # same, explicit
//	ig-telegram post <url>               # start a SinglePostWorkflow and wait
//	ig-telegram profile <username> [limit] [reels]
//
// Environment variables match the tts9000 credential keys
// (TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID, ALLOWED_USERS) so the same
// ExternalSecret can be mounted.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cterence/homelab-gitops/image-builds/ig-telegram/instagram"
	"github.com/cterence/homelab-gitops/image-builds/ig-telegram/telegram"
	"github.com/cterence/homelab-gitops/image-builds/ig-telegram/workflows"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(os.Args[1:]); err != nil {
		logger.Error("fatal error", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	logger := slog.Default()

	cfg := LoadConfig()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Temporal client.
	c, err := client.Dial(client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
	})
	if err != nil {
		return fmt.Errorf("dialing temporal: %w", err)
	}
	defer c.Close()

	// The worker always runs so that workflows have a poller to execute them.
	igClient := instagram.NewClient()
	defer igClient.Close()

	// Sender is nil when no token is set; activities that need it will fail
	// with a clear error if invoked without a sender.
	var sender *telegram.Sender
	if cfg.TelegramBotToken != "" {
		sender, err = telegram.NewSender(cfg.TelegramBotToken)
		if err != nil {
			return fmt.Errorf("creating telegram sender: %w", err)
		}
	}

	activities := &workflows.Activities{
		IG: igClient,
		TG: sender,
	}

	w := worker.New(c, cfg.TaskQueue, worker.Options{})
	w.RegisterWorkflow(workflows.SinglePostWorkflow)
	w.RegisterWorkflow(workflows.ProfileBatchWorkflow)
	w.RegisterActivity(activities.FetchProfileURLsActivity)
	w.RegisterActivity(activities.ScrapePostMediaActivity)
	w.RegisterActivity(activities.DownloadMediaActivity)
	w.RegisterActivity(activities.SendTelegramMediaActivity)
	w.RegisterActivity(activities.CleanupFileActivity)

	if err := w.Start(); err != nil {
		return fmt.Errorf("starting temporal worker: %w", err)
	}
	defer w.Stop()
	logger.Info("temporal worker started", "task_queue", cfg.TaskQueue, "address", cfg.TemporalAddress)

	// Determine subcommand. Default is "bot" for backwards compatibility.
	cmd := "bot"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "bot":
		if err := cfg.ValidateForBot(); err != nil {
			return err
		}
		return runBot(ctx, c, cfg, logger)
	case "post":
		if err := cfg.ValidateForCLI(); err != nil {
			return err
		}
		return runPostCLI(ctx, c, cfg, args)
	case "profile":
		if err := cfg.ValidateForCLI(); err != nil {
			return err
		}
		return runProfileCLI(ctx, c, cfg, args)
	default:
		return fmt.Errorf("unknown command %q: use 'bot', 'post <url>', or 'profile <username> [limit] [reels]'", cmd)
	}
}

// runBot starts the Telegram bot polling loop.
func runBot(ctx context.Context, c client.Client, cfg Config, logger *slog.Logger) error {
	bot, err := telegram.NewBot(cfg.TelegramBotToken, c, cfg.AllowedUserID, cfg.TaskQueue, logger)
	if err != nil {
		return fmt.Errorf("creating telegram bot: %w", err)
	}

	if err := bot.Start(ctx); err != nil {
		return fmt.Errorf("telegram bot stopped: %w", err)
	}
	return nil
}

// runPostCLI starts a SinglePostWorkflow and waits for the result.
func runPostCLI(ctx context.Context, c client.Client, cfg Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: ig-telegram post <url>")
	}
	postURL := args[0]
	if !instagram.IsPostURL(postURL) {
		return fmt.Errorf("not a valid instagram post/reel URL: %s", postURL)
	}

	workflowID := fmt.Sprintf("ig-post-cli-%d", time.Now().Unix())
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: cfg.TaskQueue,
	}, "SinglePostWorkflow", cfg.TelegramChatID, postURL)
	if err != nil {
		return fmt.Errorf("starting workflow: %w", err)
	}

	fmt.Printf("started workflow %s (runID %s)\n", run.GetID(), run.GetRunID())

	var result string
	if err := run.Get(ctx, &result); err != nil {
		return fmt.Errorf("workflow failed: %w", err)
	}

	fmt.Printf("result: %s\n", result)
	return nil
}

// runProfileCLI starts a ProfileBatchWorkflow and waits for the result.
func runProfileCLI(ctx context.Context, c client.Client, cfg Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: ig-telegram profile <username> [limit] [reels]")
	}

	username, limit, onlyReels, err := telegram.ParseProfileCommand("/profile " + strings.Join(args, " "))
	if err != nil {
		return err
	}

	profileURL := instagram.ProfileURL(username)
	workflowID := fmt.Sprintf("ig-profile-cli-%d", time.Now().Unix())
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: cfg.TaskQueue,
	}, "ProfileBatchWorkflow", cfg.TelegramChatID, profileURL, limit, onlyReels)
	if err != nil {
		return fmt.Errorf("starting workflow: %w", err)
	}

	fmt.Printf("started workflow %s (runID %s)\n", run.GetID(), run.GetRunID())
	fmt.Printf("fetching up to %d posts from @%s (reels only: %v)\n", limit, username, onlyReels)

	var result string
	if err := run.Get(ctx, &result); err != nil {
		return fmt.Errorf("workflow failed: %w", err)
	}

	fmt.Printf("result: %s\n", result)
	return nil
}
