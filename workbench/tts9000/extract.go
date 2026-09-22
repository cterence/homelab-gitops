package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// errBlocked marks extraction failures caused by Cloudflare-style
// protection, so the bot can reply with a friendlier message.
var errBlocked = errors.New("access blocked")

const extractUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// extractArticleText fetches the URL and returns the page's text content,
// mirroring BeautifulSoup's get_text(separator="\n", strip=True).
func extractArticleText(ctx context.Context, pageURL string, httpClient *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("building request for %s: %w", pageURL, err)
	}

	req.Header.Set("User-Agent", extractUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed for %s: %w", pageURL, err)
	}
	// The body is fully read below; nothing to propagate from Close.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", extractionError(resp.StatusCode, pageURL)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parsing HTML from %s: %w", pageURL, err)
	}

	var (
		pieces []string
		walk   func(*html.Node)
	)

	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			pieces = append(pieces, strings.TrimSpace(n.Data))
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return strings.Join(pieces, "\n"), nil
}

func extractionError(status int, pageURL string) error {
	switch {
	case status == http.StatusForbidden:
		return fmt.Errorf("%w (403) for %s. Site may have Cloudflare or similar protection", errBlocked, pageURL)
	case status >= 400 && status < 500:
		return fmt.Errorf("client error (%d) for %s", status, pageURL)
	case status >= 500:
		return fmt.Errorf("server error (%d) for %s", status, pageURL)
	default:
		return fmt.Errorf("http error (%d) for %s", status, pageURL)
	}
}
