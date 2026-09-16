package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/antchfx/htmlquery"
	"github.com/cenkalti/backoff/v5"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/goccy/go-yaml"
)

type scrobble struct {
	artist          string
	track           string
	timestamp       time.Time
	timestampString string
	trackDuration   time.Duration
	url             string
}

type durationByTrackByArtist map[string]map[string]string

const (
	customTrackDurationsFile = "track-durations.yaml"
	browserOperationsTimeout = 30 * time.Second
	inputDayFormat           = "02-01-2006"
	lastFMQueryDayFormat     = "2006-01-02"
)

func clickConsentBanner(ctx context.Context) error {
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 15*time.Second)
	defer timeoutCancel()

	cookies, err := getCookies(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cookies: %w", err)
	}

	slog.Debug("Got cookies", "cookieCount", len(cookies))

	consentBoxCookieIndex := slices.IndexFunc(cookies, func(cookie *network.Cookie) bool {
		return cookie.Name == "OptanonAlertBoxClosed"
	})

	cookieExpired := false

	if consentBoxCookieIndex != -1 {
		slog.Debug("Consent cookie found")

		consentBoxCookie := cookies[consentBoxCookieIndex]

		cookieExpiry := cdp.TimeSinceEpoch(time.Unix(int64(consentBoxCookie.Expires), 0))
		if cookieExpiry.Time().Before(time.Now()) {
			slog.Info("Consent cookie expired, clicking on banner")

			cookieExpired = true
		}
	}

	if consentBoxCookieIndex == -1 || cookieExpired {
		err = chromedp.Run(timeoutCtx,
			chromedp.WaitVisible(`#onetrust-reject-all-handler`, chromedp.ByID),
			chromedp.Sleep(1*time.Second), // Cookie banner takes a while to come up, we don't want to miss the click
			chromedp.Click(`#onetrust-reject-all-handler`, chromedp.ByID),
			chromedp.Sleep(500*time.Millisecond), // Wait for cookie banner to disappear
		)
		if err != nil {
			if strings.Contains(err.Error(), "context deadline exceeded") {
				slog.Warn("Failed to click on cookie banner, ignoring")
				return nil
			}

			return err
		}

		slog.Debug("Clicked on cookie banner")
	} else {
		slog.Debug("Skipped clicking on cookie banner")
	}

	return nil
}

func getStartPage(c *Config) (int, error) {
	timeoutCtx, cancel := context.WithTimeout(c.taskCtx, browserOperationsTimeout)
	defer cancel()

	var (
		pageNumbers   []string
		scrobbleCount int
	)

	noScrobbles := false

	err := chromedp.Run(timeoutCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			err := chromedp.Navigate("https://www.last.fm/user/" + c.LastFMUsername + "/library").Do(ctx)
			if err != nil {
				return fmt.Errorf("failed to navigate to user library: %w", err)
			}

			if c.noLogin {
				err := clickConsentBanner(ctx)
				if err != nil {
					return fmt.Errorf("failed to click on consent banner: %w", err)
				}
			}

			query := fmt.Sprintf("https://www.last.fm/user/%s/library", c.LastFMUsername)

			url, err := url.Parse(query)
			if err != nil {
				return fmt.Errorf("failed to parse library query URL: %w", err)
			}

			if !c.From.IsZero() {
				fromExpr := c.From.Format(lastFMQueryDayFormat)
				q := url.Query()
				q.Add("from", fromExpr)
				url.RawQuery = q.Encode()
			}

			if !c.To.IsZero() {
				toExpr := c.To.Format(lastFMQueryDayFormat)
				q := url.Query()
				q.Add("to", toExpr)
				url.RawQuery = q.Encode()
			}

			err = chromedp.Navigate(url.String()).Do(ctx)
			if err != nil {
				return fmt.Errorf("failed to navigate to user library with from / to dates: %w", err)
			}

			err = chromedp.WaitVisible(`//h1[@class='content-top-header']`, chromedp.BySearch).Do(ctx)
			if err != nil {
				return fmt.Errorf("failed to wait for h1 with content-top-header class: %w", err)
			}

			var noDataNodes []*cdp.Node

			err = chromedp.Nodes(`//p[@class='no-data-message']`, &noDataNodes, chromedp.AtLeast(0)).Do(ctx)
			if err != nil {
				return fmt.Errorf("failed to get no-data-message p element: %w", err)
			}
			// Early return if we find no scrobble
			if len(noDataNodes) == 1 {
				noScrobbles = true
				return nil
			}

			var scrobbleCountStr string

			err = chromedp.Text(`//h2[@class='metadata-title' and text()='Scrobbles']/../p`, &scrobbleCountStr, chromedp.BySearch).Do(ctx)
			if err != nil {
				return fmt.Errorf("failed to get scrobble count: %w", err)
			}

			scrobbleCountStr = strings.ReplaceAll(scrobbleCountStr, ",", "")

			scrobbleCount, err = strconv.Atoi(scrobbleCountStr)
			if err != nil {
				return fmt.Errorf("failed to convert scrobble count to int: %w", err)
			}

			if c.StartPage != 0 && scrobbleCount > 50 {
				scrobbleCount = c.StartPage * 50
			}

			slog.Info("Scrobbles to process", "count", scrobbleCount)

			if scrobbleCount > 50 {
				err = chromedp.Evaluate(`[...document.querySelectorAll('.pagination-page')].map((e) => e.innerText)`, &pageNumbers).Do(ctx)
				if err != nil {
					return fmt.Errorf("failed to get page numbers: %w", err)
				}
			}

			return nil
		}),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve total pages: %w", err)
	}

	if noScrobbles {
		return 0, ErrNoScrobbles
	}

	if len(pageNumbers) == 0 {
		// There is only one page with less than 50 scrobbles
		if scrobbleCount > 0 {
			return 1, nil
		}

		return 0, errors.New("no pagination found on the page")
	}

	totalPages, err := strconv.Atoi(strings.TrimSpace(strings.Split(pageNumbers[len(pageNumbers)-1], " ")[0]))
	if err != nil {
		return 0, fmt.Errorf("failed to convert total pages number: %w", err)
	}

	slog.Info("Total pages found", "pages", totalPages)

	startPage := totalPages
	if c.StartPage != 0 {
		if c.StartPage > totalPages {
			return 0, fmt.Errorf("start page %d exceeds total pages %d", c.StartPage, totalPages)
		}

		slog.Info("Starting from page", "page", c.StartPage)
		startPage = c.StartPage
	}

	slog.Info("Starting on page", "currentPage", startPage)

	return startPage, nil
}

func getUserTrackDurations(dataDir string) (durationByTrackByArtist, error) {
	customTrackDurationsBytes, err := os.ReadFile(path.Join(dataDir, customTrackDurationsFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var userTrackDurations durationByTrackByArtist
	if len(customTrackDurationsBytes) > 0 {
		err = yaml.Unmarshal(customTrackDurationsBytes, &userTrackDurations)
		if err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	return userTrackDurations, nil
}

var ErrNoScrobbles = errors.New("no scrobbles found for the selected period")

func getScrobbles(c *Config, currentPage int) ([]scrobble, error) {
	timeoutCtx, timeoutCancel := context.WithTimeout(c.taskCtx, browserOperationsTimeout)
	defer timeoutCancel()

	query := fmt.Sprintf("https://www.last.fm/user/%s/library?page=%s", c.LastFMUsername, strconv.Itoa(currentPage))

	url, err := url.Parse(query)
	if err != nil {
		return nil, fmt.Errorf("failed to parse library query URL: %w", err)
	}

	if !c.From.IsZero() {
		fromExpr := c.From.Format(lastFMQueryDayFormat)
		q := url.Query()
		q.Add("from", fromExpr)
		url.RawQuery = q.Encode()
	}

	if !c.To.IsZero() {
		toExpr := c.To.Format(lastFMQueryDayFormat)
		q := url.Query()
		q.Add("to", toExpr)
		url.RawQuery = q.Encode()
	}

	slog.Debug("get scrobble library page", "query", query)

	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate(url.String()),
		chromedp.WaitVisible(`.top-bar`, chromedp.ByQuery),
		// Remove the top bar to avoid clicking on it by accident when deleting scrobbles
		chromedp.Evaluate("let node1 = document.querySelector('.top-bar'); node1.parentNode.removeChild(node1)", nil),
		chromedp.Evaluate("let node2 = document.querySelector('.masthead'); node2.parentNode.removeChild(node2)", nil),
	)
	if err != nil {
		slog.Error("Failed to navigate to page", "page", currentPage, "error", err)
	}

	var scrobbleRows []string

	err = chromedp.Run(timeoutCtx,
		chromedp.Evaluate(`[...document.querySelectorAll('.chartlist-row')].map((e) => e.outerHTML)`, &scrobbleRows),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve scrobble rows: %w", err)
	}

	scrobbles := []scrobble{}

	slog.Info("Scrobbles found on page", "count", len(scrobbleRows))

	for _, row := range scrobbleRows {
		scrobble, err := generateScrobble(row)
		if err != nil {
			slog.Error("Failed to generate scrobble", "error", err)
			continue
		}

		slog.Debug("Generated scrobble", "artist", scrobble.artist, "track", scrobble.track, "timestamp", scrobble.timestamp)
		scrobbles = append(scrobbles, scrobble)
	}

	slices.Reverse(scrobbles)

	return scrobbles, nil
}

func generateScrobble(row string) (scrobble, error) {
	// Execute xpath on the row
	var (
		artist       string
		track        string
		timestamp    time.Time
		timestampStr string
		scrobbleURL  string
	)

	doc, err := htmlquery.Parse(strings.NewReader("<table><tbody>" + row + "</tbody></table>"))
	if err != nil {
		return scrobble{}, fmt.Errorf("failed to parse row HTML: %w", err)
	}

	artistNode := htmlquery.FindOne(doc, `.//input[@name='artist_name']`)
	if artistNode != nil {
		artist = strings.TrimSpace(htmlquery.SelectAttr(artistNode, "value"))
	} else {
		return scrobble{}, fmt.Errorf("artist not found in row: %s", row)
	}

	trackNode := htmlquery.FindOne(doc, `.//input[@name='track_name']`)
	if trackNode != nil {
		track = strings.TrimSpace(htmlquery.SelectAttr(trackNode, "value"))
	} else {
		return scrobble{}, fmt.Errorf("track not found in row: %s", row)
	}

	timestampNode := htmlquery.FindOne(doc, `.//input[@name='timestamp']`)
	if timestampNode != nil {
		timestampStr = strings.TrimSpace(htmlquery.SelectAttr(timestampNode, "value"))
		// Timestamp is 1754948517
		timestampInt, err := strconv.ParseInt(timestampStr, 10, 64)
		if err != nil {
			return scrobble{}, fmt.Errorf("failed to parse timestamp: %w", err)
		}

		timestamp = time.Unix(timestampInt, 0)
	} else {
		return scrobble{}, fmt.Errorf("timestamp not found in row: %s", row)
	}

	urlNode := htmlquery.FindOne(doc, `.//td[contains(@class,'chartlist-name')]/a`)
	if urlNode != nil {
		scrobblePath := strings.TrimSpace(htmlquery.SelectAttr(urlNode, "href"))

		scrobbleParsedURL, err := url.Parse("https://www.last.fm" + scrobblePath)
		if err != nil {
			return scrobble{}, fmt.Errorf("failed to parse scrobble url: %w", err)
		}

		scrobbleURL = scrobbleParsedURL.String()
	} else {
		return scrobble{}, errors.New("url not found in row")
	}

	return scrobble{
		artist:          artist,
		track:           track,
		timestamp:       timestamp,
		timestampString: timestampStr,
		url:             scrobbleURL,
	}, nil
}

var ErrUnknownTrackAlreadyInMap = errors.New("no duration found in cache or MusicBrainz API, track already saved in unknown track durations")

func getTrackDuration(ctx context.Context, c *Config, userTrackDurations durationByTrackByArtist, s *scrobble) error {
	// Check if track is in userTrackDurations
	if userTrackDurations != nil && userTrackDurations[s.artist] != nil && userTrackDurations[s.artist][s.track] != "" {
		// Convert to duration with 4m0s format
		trackDuration, err := time.ParseDuration(userTrackDurations[s.artist][s.track])
		if err != nil {
			// A malformed entry would silently produce a zero duration and disable
			// duplicate detection for the track, so skip the scrobble instead.
			return fmt.Errorf("invalid duration %q in track durations for %s - %s: %w", userTrackDurations[s.artist][s.track], s.artist, s.track, err)
		}

		s.trackDuration = trackDuration
		slog.Debug("Found track duration in user track durations", "artist", s.artist, "track", s.track, "duration", s.trackDuration)

		return nil
	}

	if c.unknownTrackDurations[s.artist] != nil {
		if _, found := c.unknownTrackDurations[s.artist][s.track]; found {
			return ErrUnknownTrackAlreadyInMap
		}
	}

	query := fmt.Sprintf(`artist:"%s" AND recording:"%s"`, s.artist, s.track)
	// Hash the query
	queryHasher := sha256.New()
	queryHasher.Write([]byte(query))
	cacheKey := fmt.Sprintf("mbquery:%x", queryHasher.Sum(nil))

	cacheGetStartTime := time.Now()
	cachedTrackDuration, err := c.cache.Get(ctx, cacheKey)
	slog.Debug("Cache get", "took", time.Since(cacheGetStartTime), "key", cacheKey)

	if err != nil {
		if errors.Is(err, ErrCacheMiss) {
			c.runStats.cacheMisses++

			slog.Debug("Cache miss for track duration query", "artist", s.artist, "track", s.track)

			trackDuration, err := backoff.Retry(ctx, func() (time.Duration, error) {
				return getTrackDurationFromMusicBrainz(c, s.artist, s.track)
			}, backoff.WithBackOff(backoff.NewExponentialBackOff()), backoff.WithMaxTries(10))
			if err != nil {
				return fmt.Errorf("failed to get track duration from MusicBrainz API: %w", err)
			}

			if trackDuration == 0 {
				trackDuration, err = getTrackDurationFromLastFM(c, s.url)
				if err != nil {
					slog.Warn("Could not get track duration from Last.fm", "error", err, "scrobbleURL", s.url)
				}
			}

			if trackDuration <= 0 {
				return addToUnknownTrackDurations(c, s.artist, s.track)
			}

			s.trackDuration = trackDuration
			cacheTrackDuration(ctx, c, cacheKey, trackDuration)
			slog.Debug("Found track duration", "artist", s.artist, "track", s.track, "duration", s.trackDuration)

			return nil
		}

		return fmt.Errorf("failed to get cached track duration: %w", err)
	}

	c.runStats.cacheHits++

	s.trackDuration, err = time.ParseDuration(cachedTrackDuration)
	if err != nil {
		return fmt.Errorf("failed to parse cached track duration: %w", err)
	}

	if s.trackDuration <= 0 {
		cacheDeleteStartTime := time.Now()

		if err := c.cache.Delete(ctx, cacheKey); err != nil {
			slog.Warn("Failed to delete cache entry", "key", cacheKey)
		}

		slog.Debug("Cache delete", "took", time.Since(cacheDeleteStartTime), "key", cacheKey)

		return addToUnknownTrackDurations(c, s.artist, s.track)
	}

	slog.Debug("Cache hit for track duration query", "artist", s.artist, "track", s.track, "duration", s.trackDuration)

	return nil
}

func addToUnknownTrackDurations(c *Config, artist, track string) error {
	if c.unknownTrackDurations[artist] == nil {
		c.unknownTrackDurations[artist] = make(map[string]string)
	}

	if _, found := c.unknownTrackDurations[artist][track]; !found {
		c.unknownTrackDurations[artist][track] = ""
		c.runStats.unknownTrackDurationsCount++
	} else {
		return ErrUnknownTrackAlreadyInMap
	}

	return fmt.Errorf("track %s - %s, saved to unknown track durations", artist, track)
}

func getTrackDurationFromMusicBrainz(c *Config, artist, track string) (time.Duration, error) {
	query := fmt.Sprintf(`artist:"%s" AND recording:"%s"`, artist, track)

	resp, err := c.mb.SearchRecording(query, -1, -1)
	if err != nil {
		return 0, fmt.Errorf("failed to search MusicBrainz: %w", err)
	}

	if len(resp.Recordings) == 0 {
		// Not found, don't return an error and skip setting cache
		return 0, nil
	}

	if len(resp.Recordings) > 1 {
		slog.Debug("Multiple MusicBrainz recordings found, using the first one", "query", query, "count", len(resp.Recordings))

		for i, rec := range resp.Recordings {
			slog.Debug("Recording", "index", i, "artist", rec.ArtistCredit.NameCredits, "track", rec.Title, "duration", rec.Length)
		}
	}

	duration := time.Duration(resp.Recordings[0].Length) * time.Millisecond

	return duration, nil
}

func getTrackDurationFromLastFM(c *Config, url string) (time.Duration, error) {
	var duration time.Duration

	timeoutCtx, cancel := context.WithTimeout(c.taskCtx, browserOperationsTimeout)
	defer cancel()

	ctx, cancel := chromedp.NewContext(timeoutCtx)
	defer cancel()

	trackDurationText := ""

	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`//div[@class='header-new-content']`, chromedp.BySearch),
		chromedp.Evaluate(`[...document.querySelectorAll('.catalogue-metadata-heading')].find((e) => e.innerText == "Length")?.nextElementSibling?.innerText`, &trackDurationText),
	)
	if err != nil {
		if err.Error() == "encountered an undefined value" {
			return duration, nil
		}

		return duration, err
	}

	duration, err = time.ParseDuration(strings.ReplaceAll(trackDurationText, ":", "m") + "s")
	if err != nil {
		return duration, err
	}

	slog.Debug("Parsed duration from last.fm", "trackDurationText", trackDurationText, "calculatedDuration", duration)

	return duration, nil
}

func cacheTrackDuration(ctx context.Context, c *Config, cacheKey string, duration time.Duration) {
	cacheSetStartTime := time.Now()
	err := c.cache.Set(ctx, cacheKey, duration.String())
	slog.Debug("Cache set", "took", time.Since(cacheSetStartTime), "key", cacheKey)

	if err != nil {
		slog.Error("Failed to cache track duration", "error", err)
	}
}

func processScrobblesFromStartToEndPage(ctx context.Context, c *Config, startPage int, endPage int, userTrackDurations durationByTrackByArtist) error {
	for currentPage := startPage; currentPage >= endPage; currentPage-- {
		slog.Info("Processing page", "page", currentPage)

		scrobbles, err := backoff.Retry(ctx, func() ([]scrobble, error) {
			return getScrobbles(c, currentPage)
		}, backoff.WithMaxTries(3))
		if err != nil {
			return err
		}

		var previousScrobble *scrobble
		for _, currentScrobble := range scrobbles {
			previousScrobble = processPreviousAndCurrentScrobbles(ctx, c, previousScrobble, &currentScrobble, userTrackDurations)
			c.runStats.processedScrobbles++
		}
	}

	return nil
}

func processPreviousAndCurrentScrobbles(ctx context.Context, c *Config, previousScrobble *scrobble, currentScrobble *scrobble, userTrackDurations durationByTrackByArtist) *scrobble {
	err := getTrackDuration(ctx, c, userTrackDurations, currentScrobble)
	if err != nil {
		if !errors.Is(err, ErrUnknownTrackAlreadyInMap) {
			slog.Warn("failed to get track duration, skipping scrobble", "error", err)
		}

		c.runStats.skippedScrobbleUnknownDuration++

		return currentScrobble
	}

	slog.Debug("Track duration found", "artist", currentScrobble.artist, "track", currentScrobble.track, "duration", currentScrobble.trackDuration)

	if previousScrobble != nil {
		isDuplicate, err := detectDuplicateScrobble(c, previousScrobble, currentScrobble)
		if err != nil {
			slog.Warn("failed to detect duplicated scrobble", "error", err)
			return currentScrobble
		}

		if isDuplicate {
			c.deletedScrobbles = append(c.deletedScrobbles, currentScrobble)
			if c.CanDelete {
				if err := deleteScrobbleWithRetries(ctx, c, previousScrobble.timestampString, false, 3); err != nil {
					slog.Warn("failed to delete scrobble", "error", err)
				}

				slog.Info("Previous scrobble deleted", "artist", currentScrobble.artist, "track", currentScrobble.track, "timestamp", previousScrobble.timestamp)
			}

			return currentScrobble
		}

		if c.CompleteThreshold > 0 {
			isIncomplete, err := detectIncompleteScrobble(c, previousScrobble, currentScrobble)
			if err != nil {
				slog.Warn("failed to detect incomplete scrobble", "error", err)
				return currentScrobble
			}

			if isIncomplete {
				c.deletedScrobbles = append(c.deletedScrobbles, currentScrobble)
				if c.CanDelete {
					if err := deleteScrobbleWithRetries(ctx, c, currentScrobble.timestampString, true, 3); err != nil {
						slog.Warn("failed to delete scrobble", "error", err)
						return currentScrobble
					}

					slog.Info("Current scrobble deleted", "artist", currentScrobble.artist, "track", currentScrobble.track, "timestamp", currentScrobble.timestamp)
				}

				return previousScrobble
			}
		}
	}

	return currentScrobble
}

func detectDuplicateScrobble(c *Config, previousScrobble *scrobble, currentScrobble *scrobble) (bool, error) {
	if currentScrobble.artist == previousScrobble.artist && currentScrobble.track == previousScrobble.track && currentScrobble.timestamp != previousScrobble.timestamp {
		currentScrobbleDuration := currentScrobble.timestamp.Sub(previousScrobble.timestamp)
		currentScrobbleCompletionPercentage := min((float64(currentScrobbleDuration)/float64(currentScrobble.trackDuration))*100, 100)
		duplicateDurationThreshold := time.Duration(float64(currentScrobble.trackDuration) * float64(c.DuplicateThreshold) / 100.0)
		isDuplicate := currentScrobbleCompletionPercentage < float64(c.DuplicateThreshold)

		slog.Debug("duplicate scrobble detection calculations", "previousScrobbleTimestamp", previousScrobble.timestamp, "currentScrobbleTimestamp", currentScrobble.timestamp, "currentScrobbleDuration", currentScrobbleDuration, "duplicateThreshold", c.DuplicateThreshold, "duplicateDurationThreshold", duplicateDurationThreshold, "currentScrobbleCompletionPercentage", currentScrobbleCompletionPercentage, "isDuplicate", isDuplicate)

		if isDuplicate {
			slog.Info("🎯 Duplicate scrobble detected!", "artist", currentScrobble.artist, "track", currentScrobble.track, "duration", currentScrobble.trackDuration, "timeBetweenScrobbles", duplicateDurationThreshold, "scrobbleToDeleteTimestamp", previousScrobble.timestamp.Format(time.RFC822))
			return true, nil
		}
	}

	return false, nil
}

func detectIncompleteScrobble(c *Config, previousScrobble *scrobble, currentScrobble *scrobble) (bool, error) {
	currentScrobbleDuration := currentScrobble.timestamp.Sub(previousScrobble.timestamp)
	currentScrobbleCompletionPercentage := min((float64(currentScrobbleDuration)/float64(currentScrobble.trackDuration))*100, 100)
	completeDurationThreshold := time.Duration(float64(currentScrobble.trackDuration) * float64(c.CompleteThreshold) / 100.0)
	isIncomplete := currentScrobbleCompletionPercentage < float64(c.CompleteThreshold)

	slog.Debug("incomplete scrobble detection calculations", "previousScrobbleTimestamp", previousScrobble.timestamp, "currentTrackDuration", currentScrobble.trackDuration, "currentScrobbleTimestamp", currentScrobble.timestamp, "currentScrobbleDuration", currentScrobbleDuration, "completeThreshold", c.CompleteThreshold, "completeDurationThreshold", completeDurationThreshold, "currentScrobbleCompletionPercentage", currentScrobbleCompletionPercentage, "isIncomplete", isIncomplete)

	if isIncomplete {
		slog.Info("⏳ Incomplete scrobble detected!", "artist", currentScrobble.artist, "track", currentScrobble.track, "previousScrobbleTimestamp", previousScrobble.timestamp, "currentScrobbleTimestamp", currentScrobble.timestamp)
		return true, nil
	}

	return false, nil
}

func deleteScrobble(c *Config, timestamp string, deleteCurrentScrobble bool) error {
	timeoutCtx, cancel := context.WithTimeout(c.taskCtx, 3*time.Second)
	defer cancel()

	// Sometimes two scrobbles have an identical timestamp
	// Depending on if we want to delete the previous or the current scrobble, we modify the xpath expression
	xpathPrefix := `(//input[@value='` + timestamp + `'])`
	if deleteCurrentScrobble {
		xpathPrefix += `[last()]`
	}

	slog.Debug("Attempting to delete scrobble", "timestamp", timestamp, "xpath", xpathPrefix)

	err := chromedp.Run(timeoutCtx,
		// Click away to close any previous popup
		chromedp.MouseClickXY(0, 0),
		chromedp.Click(xpathPrefix+`/../../../../button`, chromedp.BySearch),
		chromedp.WaitVisible(`//tr[contains(@class,'show-focus-controls')]`, chromedp.BySearch),
		chromedp.Click(xpathPrefix+`/../../../../button`, chromedp.BySearch),
		chromedp.WaitVisible(xpathPrefix+`/../button`, chromedp.BySearch),
		chromedp.Click(xpathPrefix+`/../button`, chromedp.BySearch),
	)
	if err != nil {
		return fmt.Errorf("failed delete scrobble: %w", err)
	}

	return nil
}

func deleteScrobbleWithRetries(ctx context.Context, c *Config, timestamp string, deleteCurrentScrobble bool, retryCount uint) error {
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		return struct{}{}, deleteScrobble(c, timestamp, deleteCurrentScrobble)
	}, backoff.WithMaxTries(retryCount))
	if err != nil {
		c.runStats.scrobbleDeleteFails++
		return err
	}

	return nil
}

func logStats(ctx context.Context, c *Config) error {
	c.runStats.elapsedTime = time.Since(c.startTime)

	telegramMessage := fmt.Sprintf("Run of %s\n", c.startTime.Format(time.RFC1123))

	var deletedScrobblesStat string
	if c.CanDelete {
		deletedScrobblesStat = fmt.Sprintf("Duplicated scrobbles deleted: %d", len(c.deletedScrobbles))
	} else {
		deletedScrobblesStat = fmt.Sprintf("Duplicated scrobbles not deleted: %d", len(c.deletedScrobbles))
	}

	messages := []string{
		"Run statistics:",
		deletedScrobblesStat,
		fmt.Sprintf("MusicBrainz API cache hits: %d", c.runStats.cacheHits),
		fmt.Sprintf("MusicBrainz API cache misses: %d", c.runStats.cacheMisses),
		fmt.Sprintf("Scrobbles processed: %d", c.runStats.processedScrobbles),
		fmt.Sprintf("Unknown duration track count: %d", c.runStats.unknownTrackDurationsCount),
		fmt.Sprintf("Scrobbles skipped due to unknown track duration: %d", c.runStats.skippedScrobbleUnknownDuration),
		fmt.Sprintf("Scrobbles not deleted due to error: %d", c.runStats.scrobbleDeleteFails),
		fmt.Sprintf("Elapsed time: %s", c.runStats.elapsedTime.Truncate(time.Millisecond/10)),
	}

	for _, m := range messages {
		slog.Info(m)
		telegramMessage = strings.Join([]string{telegramMessage, m}, "\n")
	}

	if c.telegramBot != nil {
		if err := sendTelegramMessage(ctx, c, telegramMessage); err != nil {
			return fmt.Errorf("failed to send telegram message: %w", err)
		}

		slog.Info("Sent telegram message")
	}

	return nil
}

func writeUnknownTrackDurations(unknownTrackDurations durationByTrackByArtist, dataDir string) error {
	userTrackDurations, err := getUserTrackDurations(dataDir)
	if err != nil {
		return err
	}

	for artist, tracks := range userTrackDurations {
		if unknownTrackDurations[artist] == nil {
			unknownTrackDurations[artist] = make(map[string]string)
		}

		maps.Copy(unknownTrackDurations[artist], tracks)
	}

	bytes, err := yaml.Marshal(unknownTrackDurations)
	if err != nil {
		return fmt.Errorf("failed to marshal unknown track durations to YAML: %w", err)
	}

	bytes = append([]byte("# This file lists tracks that the program could not find a duration for using the MusicBrainz API\n# If a track has an unknown duration, this program will never delete its duplicate scrobbles\n# Specify the duration of each track using the Go time ParseDuration format (ex: 5m06s), then rerun the program\n# You may use it to override a track length, but you must strictly match the scrobble's artist and track name\n\n"), bytes...)

	file, err := os.OpenFile(path.Join(dataDir, customTrackDurationsFile), os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			slog.Warn("Failed to save unknown track durations in "+customTrackDurationsFile, "error", err)
			slog.Info(fmt.Sprintf(`Save the following YAML in a file named "%s" in this program's directory and follow the instructions`, customTrackDurationsFile))
			fmt.Println("\n" + string(bytes))

			return nil
		}

		return fmt.Errorf("failed to open unknown track durations file: %w", err)
	}

	_, err = file.Write(bytes)
	if err != nil {
		return fmt.Errorf("failed to save unknown track durations file: %w", err)
	}

	slog.Info("Unknown track durations saved to file", "file", file.Name())

	return nil
}

func exportScrobblesToCSV(c *Config, baseFilename string) {
	timestamp := c.startTime.Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s.csv", baseFilename, timestamp)

	slices.SortFunc(c.deletedScrobbles, func(s1, s2 *scrobble) int {
		return s1.timestamp.Compare(s2.timestamp)
	})

	file, err := os.Create(path.Join(c.DataDir, filename))
	if err != nil {
		slog.Warn("⚠️ Could not create deleted scrobble file, falling back to logging scrobbles as CSV", "file", filename, "error", err)
		logScrobblesCSV(c.deletedScrobbles)

		return
	}
	defer CloseFile(file)

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// header
	_ = writer.Write([]string{"Artist", "Track", "Timestamp", "TimestampString"})

	for _, s := range c.deletedScrobbles {
		record := []string{
			s.artist,
			s.track,
			s.timestamp.Format(time.RFC3339),
			s.timestampString,
		}
		_ = writer.Write(record)
	}

	if c.CanDelete {
		slog.Info("Deleted scrobbles saved to file", "file", file.Name())
	} else {
		slog.Info("Would-be deleted scrobbles saved to file", "file", file.Name())
	}
}

func logScrobblesCSV(scrobbles []*scrobble) {
	var sb strings.Builder

	// header
	sb.WriteString("Artist,Track,Timestamp,TimestampString\n")

	for _, s := range scrobbles {
		fmt.Fprintf(&sb, "%s,%s,%s,%s\n",
			s.artist,
			s.track,
			s.timestamp.Format(time.RFC3339),
			s.timestampString)
	}

	fmt.Printf("Scrobbles CSV:\n%s", sb.String())
}

func finishRun(ctx context.Context, c *Config) error {
	defer c.close()

	if err := logStats(ctx, c); err != nil {
		return fmt.Errorf("failed to log stats: %w", err)
	}

	if len(c.unknownTrackDurations) > 0 {
		err := writeUnknownTrackDurations(c.unknownTrackDurations, c.DataDir)
		if err != nil {
			return err
		}
	}

	if len(c.deletedScrobbles) > 0 {
		exportScrobblesToCSV(c, "deleted-scrobbles")
	}

	return nil
}

func sendTelegramMessage(ctx context.Context, c *Config, message string) error {
	params := &bot.SendMessageParams{
		ParseMode: models.ParseModeMarkdown,
		ChatID:    c.TelegramChatID,
		Text:      bot.EscapeMarkdown(message),
	}

	_, err := c.telegramBot.SendMessage(ctx, params)
	if err != nil {
		return err
	}

	return nil
}
