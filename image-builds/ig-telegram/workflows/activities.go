// Package workflows defines Temporal activities and workflows for the
// Instagram Telegram downloader.
package workflows

import (
	"context"
	"fmt"
	"os"

	"github.com/cterence/homelab-gitops/image-builds/ig-telegram/instagram"
	"github.com/cterence/homelab-gitops/image-builds/ig-telegram/telegram"
	"go.temporal.io/sdk/activity"
)

// Activities holds the dependencies needed by Temporal activities.
// Methods on this struct are registered with the Temporal worker.
type Activities struct {
	IG *instagram.Client
	TG *telegram.Sender
}

// FetchProfileURLsActivity scrapes an Instagram profile page and returns
// post/reel links up to the given limit.
func (a *Activities) FetchProfileURLsActivity(ctx context.Context, profileURL string, limit int, onlyReels bool) ([]string, error) {
	posts, err := a.IG.FetchProfilePosts(ctx, profileURL, instagram.ProfileFilter{
		MaxPosts:  limit,
		OnlyReels: onlyReels,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching profile posts: %w", err)
	}
	return posts, nil
}

// ScrapePostMediaActivity navigates to an Instagram post/reel URL and
// extracts direct CDN media links.
func (a *Activities) ScrapePostMediaActivity(ctx context.Context, postURL string) ([]instagram.MediaItem, error) {
	items, err := a.IG.FetchMediaURLs(ctx, postURL)
	if err != nil {
		return nil, fmt.Errorf("scraping post media: %w", err)
	}
	return items, nil
}

// DownloadMediaActivity downloads a CDN media URL to local disk and returns
// the file path.
func (a *Activities) DownloadMediaActivity(ctx context.Context, item instagram.MediaItem) (string, error) {
	path, err := instagram.DownloadFile(ctx, item.URL)
	if err != nil {
		return "", fmt.Errorf("downloading media: %w", err)
	}
	return path, nil
}

// SendTelegramMediaActivity sends a local file to a Telegram chat and
// cleans up the file afterwards. If no sender is configured, the file is
// kept on disk and its path is returned so the caller can handle it.
func (a *Activities) SendTelegramMediaActivity(ctx context.Context, chatID int64, localPath string, isVideo bool) (string, error) {
	if a.TG == nil {
		activity.GetLogger(ctx).Info("no telegram sender configured; file left on disk", "path", localPath)
		return localPath, nil
	}
	if err := a.TG.SendMedia(ctx, chatID, localPath, isVideo); err != nil {
		return "", fmt.Errorf("sending telegram media: %w", err)
	}
	return "", nil
}

// CleanupFileActivity removes a local file. Used as a cleanup step on
// download failures.
func (a *Activities) CleanupFileActivity(ctx context.Context, path string) error {
	if path != "" {
		return os.Remove(path)
	}
	return nil
}
