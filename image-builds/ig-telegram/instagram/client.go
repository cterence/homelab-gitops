package instagram

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Client manages a shared go-rod browser instance for Instagram scraping.
// It is safe for concurrent use: each call to page() acquires the browser
// and creates a new page (tab).
type Client struct {
	browser *rod.Browser
	mu      sync.Mutex
}

func NewClient() *Client {
	return &Client{}
}

// launch starts a new headless Chromium instance configured for container
// environments. We explicitly look up the system-installed Chromium binary
// via launcher.LookPath(); without this, go-rod tries to download its own
// Chromium which fails on arm64 containers.
func (c *Client) launch() (*rod.Browser, error) {
	l := launcher.New().
		Headless(true).
		Set("disable-gpu")

	if bin, ok := launcher.LookPath(); ok {
		l = l.Bin(bin)
	}

	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launching browser: %w", err)
	}

	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		return nil, fmt.Errorf("connecting to browser: %w", err)
	}
	return b, nil
}

// page creates a new browser page, navigating to url. If the browser is not
// running or has crashed, it is (re)launched.
func (c *Client) page(url string) (*rod.Page, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.browser == nil {
		b, err := c.launch()
		if err != nil {
			return nil, err
		}
		c.browser = b
	}

	page, err := c.browser.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		// Browser may be dead; reset so the next call relaunches.
		c.browser = nil
		return nil, fmt.Errorf("creating page: %w", err)
	}
	return page, nil
}

// Close cleans up the browser if it is running.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.browser != nil {
		err := c.browser.Close()
		c.browser = nil
		return err
	}
	return nil
}

// FetchMediaURLs navigates to an Instagram post or reel URL and extracts
// direct CDN media links. It tries the embed page first (which has direct
// CDN URLs without JS rendering), then falls back to the main page with
// network interception.
func (c *Client) FetchMediaURLs(ctx context.Context, postURL string) ([]MediaItem, error) {
	// Try the embed page first — it's a lightweight HTML page with direct
	// media URLs, no JS rendering required.
	if embedURL, ok := embedURL(postURL); ok {
		items, err := c.scrapeEmbedPage(embedURL)
		if err == nil && len(items) > 0 {
			return items, nil
		}
	}

	// Fall back to the main page with network interception + DOM extraction.
	return c.scrapeMainPage(postURL)
}

// embedURL converts an Instagram post/reel URL to its embed URL.
// e.g. https://www.instagram.com/reel/DbU8t1sNW2G/ -> https://www.instagram.com/reel/DbU8t1sNW2G/embed/
func embedURL(postURL string) (string, bool) {
	// Extract the /p/<id> or /reel/<id> path segment.
	for _, prefix := range []string{"/p/", "/reel/"} {
		if idx := strings.Index(postURL, prefix); idx >= 0 {
			rest := postURL[idx+len(prefix):]
			// Take everything up to the next "/" or "?".
			if end := strings.IndexAny(rest, "/?"); end >= 0 {
				rest = rest[:end]
			}
			if rest == "" {
				return "", false
			}
			return "https://www.instagram.com" + prefix + rest + "/embed/", true
		}
	}
	return "", false
}

// scrapeEmbedPage loads the Instagram embed page and extracts media from
// the simplified HTML. The embed page contains direct CDN URLs in <video>
// and <img> tags without requiring JavaScript rendering.
func (c *Client) scrapeEmbedPage(embedURL string) ([]MediaItem, error) {
	page, err := c.page(embedURL)
	if err != nil {
		return nil, err
	}
	defer page.Close()

	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: userAgent,
	}); err != nil {
		return nil, fmt.Errorf("setting user agent: %w", err)
	}

	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("waiting for embed page load: %w", err)
	}
	time.Sleep(1 * time.Second)

	// Get the page HTML and search for CDN URLs with regex.
	html, err := page.HTML()
	if err != nil {
		return nil, fmt.Errorf("getting embed HTML: %w", err)
	}

	return extractURLsFromHTML(html), nil
}

// scrapeMainPage loads the full Instagram page and uses network interception
// plus DOM extraction to find media URLs.
func (c *Client) scrapeMainPage(postURL string) ([]MediaItem, error) {
	page, err := c.page(postURL)
	if err != nil {
		return nil, err
	}
	defer page.Close()

	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: userAgent,
	}); err != nil {
		return nil, fmt.Errorf("setting user agent: %w", err)
	}

	var networkItems []MediaItem
	var networkMu sync.Mutex
	seen := map[string]bool{}

	add := func(url string, isVideo bool) {
		if url == "" || seen[url] {
			return
		}
		if strings.HasPrefix(url, "blob:") {
			return
		}
		if !strings.Contains(url, "fbcdn.net") &&
			!strings.Contains(url, "cdninstagram") &&
			!strings.Contains(url, "scontent") {
			return
		}
		seen[url] = true
		networkItems = append(networkItems, MediaItem{URL: url, IsVideo: isVideo})
	}

	waitNet := page.EachEvent(func(e *proto.NetworkResponseReceived) {
		if e.Response == nil {
			return
		}
		url := e.Response.URL
		mime := e.Response.MIMEType
		switch {
		case strings.HasPrefix(mime, "video/"):
			add(url, true)
		case strings.HasPrefix(mime, "image/") && strings.Contains(url, "fbcdn.net"):
			add(url, false)
		}
	})
	_ = waitNet

	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("waiting for page load: %w", err)
	}
	time.Sleep(3 * time.Second)

	// Try clicking the video element to trigger playback/loading.
	if els, err := page.Elements("video"); err == nil {
		for _, el := range els {
			_ = el.Click(proto.InputMouseButtonLeft, 1)
		}
	}
	time.Sleep(2 * time.Second)

	networkMu.Lock()
	items := append([]MediaItem(nil), networkItems...)
	networkMu.Unlock()

	if len(items) == 0 {
		items, err = extractMediaItems(page)
		if err != nil {
			return nil, err
		}
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("no media found at %s — the post may require login or has been removed", postURL)
	}
	return items, nil
}

// extractURLsFromHTML searches raw HTML for Instagram CDN media URLs.
// It looks for video URLs (.mp4 from fbcdn.net) and image URLs
// (.jpg/.png from fbcdn.net) in src attributes and JSON data.
// HTML entities are decoded so query parameters (&amp; -> &) work correctly.
func extractURLsFromHTML(htmlText string) []MediaItem {
	var items []MediaItem
	seen := map[string]bool{}

	add := func(url string, isVideo bool) {
		// Decode HTML entities (e.g. &amp; -> &) so query params work.
		url = html.UnescapeString(url)
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		items = append(items, MediaItem{URL: url, IsVideo: isVideo})
	}

	// Match video URLs: https://...fbcdn.net/...video...mp4?...
	videoRe := regexp.MustCompile(`https://[^"'\s]+fbcdn\.net/[^"'\s]*video[^"'\s]*\.mp4[^"'\s]*`)
	for _, match := range videoRe.FindAllString(htmlText, -1) {
		add(match, true)
	}

	// Match scontent CDN video URLs.
	scontentVideoRe := regexp.MustCompile(`https://scontent[^"'\s]+\.mp4[^"'\s]*`)
	for _, match := range scontentVideoRe.FindAllString(htmlText, -1) {
		add(match, true)
	}

	// Match image URLs from CDN — only if no video was found, since
	// the image is usually just a thumbnail of the video.
	if len(items) == 0 {
		imgRe := regexp.MustCompile(`https://[^"'\s]+fbcdn\.net/[^"'\s]+\.(?:jpg|jpeg|png)[^"'\s]*`)
		for _, match := range imgRe.FindAllString(htmlText, -1) {
			add(match, false)
		}
		scontentImgRe := regexp.MustCompile(`https://scontent[^"'\s]+\.(?:jpg|jpeg|png)[^"'\s]*`)
		for _, match := range scontentImgRe.FindAllString(htmlText, -1) {
			add(match, false)
		}
	}

	return items
}

// extractMediaItems tries multiple strategies to find direct CDN media URLs:
// 1. Open Graph meta tags (og:video, og:image) — reliable for single posts/reels.
// 2. <video> <source> tags with non-blob URLs.
// 3. JavaScript evaluation to extract from Instagram's embedded JSON data.
func extractMediaItems(page *rod.Page) ([]MediaItem, error) {
	var items []MediaItem
	seen := map[string]bool{}

	add := func(url string, isVideo bool) {
		if url == "" || seen[url] {
			return
		}
		// Skip blob URLs — they're client-side and not downloadable.
		if strings.HasPrefix(url, "blob:") {
			return
		}
		seen[url] = true
		items = append(items, MediaItem{URL: url, IsVideo: isVideo})
	}

	// Strategy 1: Open Graph meta tags.
	if els, err := page.Elements(`meta[property="og:video"], meta[property="og:video:secure_url"]`); err == nil {
		for _, el := range els {
			if content, _ := el.Attribute("content"); content != nil {
				add(*content, true)
			}
		}
	}
	if els, err := page.Elements(`meta[property="og:image"], meta[property="og:image:secure_url"]`); err == nil {
		for _, el := range els {
			if content, _ := el.Attribute("content"); content != nil {
				add(*content, false)
			}
		}
	}

	// Strategy 2: <video> <source> tags with non-blob src.
	if els, err := page.Elements("video source"); err == nil {
		for _, el := range els {
			if src, _ := el.Attribute("src"); src != nil {
				add(*src, true)
			}
		}
	}

	// Strategy 3: Extract from Instagram's embedded JSON data via JavaScript.
	jsItems, err := page.Eval(`() => {
		const items = [];

		// Check for JSON-LD data.
		document.querySelectorAll('script[type="application/ld+json"]').forEach(s => {
			try {
				const data = JSON.parse(s.textContent);
				const candidates = Array.isArray(data) ? data : [data];
				for (const d of candidates) {
					if (d.video && d.video.contentUrl) items.push({url: d.video.contentUrl, video: true});
					if (d.contentUrl) items.push({url: d.contentUrl, video: d["@type"] === "VideoObject"});
					if (d.image) {
						const imgs = Array.isArray(d.image) ? d.image : [d.image];
						for (const img of imgs) {
							const url = typeof img === 'string' ? img : img.url;
							if (url) items.push({url, video: false});
						}
					}
				}
			} catch(e) {}
		});

		// Check for video src attributes that aren't blob URLs.
		document.querySelectorAll('video').forEach(v => {
			const src = v.src || (v.querySelector('source') || {}).src;
			if (src && !src.startsWith('blob:')) items.push({url: src, video: true});
		});

		// Check for article images from CDN.
		document.querySelectorAll('article img').forEach(img => {
			const src = img.src;
			if (src && (src.includes('fbcdn.net') || src.includes('cdninstagram'))) {
				items.push({url: src, video: false});
			}
		});

		return items;
	}`)
	if err == nil {
		var jsResults []struct {
			URL   string `json:"url"`
			Video bool   `json:"video"`
		}
		if err := jsItems.Value.Unmarshal(&jsResults); err == nil {
			for _, r := range jsResults {
				add(r.URL, r.Video)
			}
		}
	}

	return items, nil
}

// FetchProfilePosts navigates to an Instagram profile and collects post/reel
// links. It scrolls to load additional posts up to filter.MaxPosts.
func (c *Client) FetchProfilePosts(ctx context.Context, profileURL string, filter ProfileFilter) ([]string, error) {
	page, err := c.page(profileURL)
	if err != nil {
		return nil, err
	}
	defer page.Close()

	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: userAgent,
	}); err != nil {
		return nil, fmt.Errorf("setting user agent: %w", err)
	}

	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("waiting for page load: %w", err)
	}
	time.Sleep(2 * time.Second)

	maxPosts := filter.MaxPosts
	if maxPosts <= 0 {
		maxPosts = 12
	}

	seen := map[string]bool{}
	var posts []string

	for scroll := 0; scroll < 20 && len(posts) < maxPosts; scroll++ {
		// Collect post links.
		selectors := []string{"a[href*='/p/']", "a[href*='/reel/']"}
		for _, sel := range selectors {
			links, err := page.Elements(sel)
			if err != nil {
				continue
			}
			for _, link := range links {
				href, _ := link.Attribute("href")
				if href == nil || *href == "" {
					continue
				}
				url := *href
				if !strings.HasPrefix(url, "https://") {
					url = "https://www.instagram.com" + url
				}
				if seen[url] {
					continue
				}
				if filter.OnlyReels && !strings.Contains(url, "/reel/") {
					continue
				}
				seen[url] = true
				posts = append(posts, url)
				if len(posts) >= maxPosts {
					break
				}
			}
			if len(posts) >= maxPosts {
				break
			}
		}

		// Scroll down to trigger lazy loading.
		if err := page.Mouse.Scroll(0, 2000, 1); err != nil {
			break
		}
		time.Sleep(1500 * time.Millisecond)
	}

	if len(posts) == 0 {
		return nil, fmt.Errorf("no posts found on profile %s — it may be private or require login", profileURL)
	}
	return posts, nil
}

// DownloadFile fetches a CDN media URL and saves it to a temporary file.
// The caller is responsible for deleting the file when done.
func DownloadFile(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://www.instagram.com/")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading media: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Determine extension from URL or content type.
	ext := ".bin"
	switch {
	case strings.Contains(url, ".mp4"):
		ext = ".mp4"
	case strings.Contains(url, ".jpg"), strings.Contains(url, ".jpeg"):
		ext = ".jpg"
	case strings.Contains(url, ".png"):
		ext = ".png"
	case strings.HasPrefix(resp.Header.Get("Content-Type"), "video/"):
		ext = ".mp4"
	case strings.HasPrefix(resp.Header.Get("Content-Type"), "image/"):
		ext = ".jpg"
	}

	f, err := os.CreateTemp(".", "ig-media-*"+ext)
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("writing media to disk: %w", err)
	}

	return f.Name(), nil
}

// ProfileURL builds the full Instagram profile URL from a username.
func ProfileURL(username string) string {
	username = strings.TrimPrefix(strings.TrimPrefix(username, "@"), "/")
	return "https://www.instagram.com/" + username + "/"
}

// IsPostURL checks whether a string is an Instagram post or reel URL.
// Handles URLs like:
//
//	https://www.instagram.com/p/ABC/
//	https://instagram.com/reel/ABC/
//	https://www.instagram.com/<username>/reel/ABC/?hl=fr
func IsPostURL(s string) bool {
	return strings.Contains(s, "/p/") ||
		strings.Contains(s, "/reel/")
}

// FileExtension returns the extension for a given media file path.
func FileExtension(path string) string {
	return filepath.Ext(path)
}
