// Package instagram provides a go-rod browser wrapper for extracting media
// URLs from public Instagram posts, reels, and profile grids.
package instagram

// MediaItem represents a single piece of media extracted from an Instagram post.
type MediaItem struct {
	URL     string
	IsVideo bool
}

// ProfileFilter controls how many posts to fetch from a profile and whether
// to filter for reels only.
type ProfileFilter struct {
	MaxPosts  int
	OnlyReels bool
}
