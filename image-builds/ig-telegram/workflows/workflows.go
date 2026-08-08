package workflows

import (
	"fmt"
	"time"

	"github.com/cterence/homelab-gitops/image-builds/ig-telegram/instagram"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// scrapeRetryPolicy is applied to all browser-scraping activities.
var scrapeRetryPolicy = temporal.RetryPolicy{
	InitialInterval:    5 * time.Second,
	BackoffCoefficient: 2.0,
	MaximumAttempts:    3,
	MaximumInterval:    30 * time.Second,
}

// SinglePostWorkflow scrapes a single Instagram post/reel and sends all
// extracted media to the given Telegram chat.
func SinglePostWorkflow(ctx workflow.Context, chatID int64, postURL string) (string, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("SinglePostWorkflow started", "url", postURL, "chat_id", chatID)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy:         &scrapeRetryPolicy,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var a *Activities

	// Step 1: Scrape the post for media URLs.
	var items []instagram.MediaItem
	if err := workflow.ExecuteActivity(ctx, a.ScrapePostMediaActivity, postURL).Get(ctx, &items); err != nil {
		return "", fmt.Errorf("scraping post: %w", err)
	}

	logger.Info("scraped media", "count", len(items), "url", postURL)

	// Step 2: Download and send each media item.
	sent := 0
	var localPaths []string
	for i, item := range items {
		// Download to local disk.
		var localPath string
		if err := workflow.ExecuteActivity(ctx, a.DownloadMediaActivity, item).Get(ctx, &localPath); err != nil {
			logger.Error("download failed, skipping", "index", i, "err", err)
			continue
		}

		// Send via Telegram (this also deletes the local file when a
		// sender is configured). When no sender is configured, the
		// activity returns the local path instead.
		var outputPath string
		if err := workflow.ExecuteActivity(ctx, a.SendTelegramMediaActivity, chatID, localPath, item.IsVideo).Get(ctx, &outputPath); err != nil {
			logger.Error("send failed, cleaning up", "index", i, "err", err)
			_ = workflow.ExecuteActivity(ctx, a.CleanupFileActivity, localPath).Get(ctx, nil)
			continue
		}
		if outputPath != "" {
			localPaths = append(localPaths, outputPath)
		}
		sent++
	}

	result := fmt.Sprintf("sent %d/%d media items from %s", sent, len(items), postURL)
	if len(localPaths) > 0 {
		result += "\ndownloaded files:"
		for _, p := range localPaths {
			result += "\n  " + p
		}
	}
	logger.Info("SinglePostWorkflow completed", "result", result)
	return result, nil
}

// ProfileBatchWorkflow fetches post links from an Instagram profile and
// processes each one as a child workflow.
func ProfileBatchWorkflow(ctx workflow.Context, chatID int64, profileURL string, limit int, onlyReels bool) (string, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("ProfileBatchWorkflow started", "profile", profileURL, "limit", limit, "reels_only", onlyReels)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         &scrapeRetryPolicy,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var a *Activities

	// Step 1: Fetch all post links from the profile.
	var postURLs []string
	if err := workflow.ExecuteActivity(ctx, a.FetchProfileURLsActivity, profileURL, limit, onlyReels).Get(ctx, &postURLs); err != nil {
		return "", fmt.Errorf("fetching profile URLs: %w", err)
	}

	logger.Info("found posts", "count", len(postURLs))

	// Step 2: Process each post as a child workflow with a delay to avoid
	// rate-limiting.
	cwo := workflow.ChildWorkflowOptions{
		TaskQueue:           workflow.GetInfo(ctx).TaskQueueName,
		WorkflowRunTimeout:  10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    2,
		},
	}
	childCtx := workflow.WithChildOptions(ctx, cwo)

	processed := 0
	for i, postURL := range postURLs {
		logger.Info("processing post", "index", i, "url", postURL)

		var result string
		if err := workflow.ExecuteChildWorkflow(childCtx, "SinglePostWorkflow", chatID, postURL).Get(childCtx, &result); err != nil {
			logger.Error("child workflow failed", "index", i, "url", postURL, "err", err)
			continue
		}
		processed++

		// Delay between posts to avoid rate-limiting (skip after the last one).
		if i < len(postURLs)-1 {
			_ = workflow.Sleep(ctx, 3*time.Second)
		}
	}

	result := fmt.Sprintf("processed %d/%d posts from %s", processed, len(postURLs), profileURL)
	logger.Info("ProfileBatchWorkflow completed", "result", result)
	return result, nil
}
