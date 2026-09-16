package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const lastFMLoginURL = "https://www.last.fm/login"
const cookieFile = "lastfm-cookies.json"

func login(ctx context.Context, c *Config) error {
	err := loadCookies(ctx, path.Join(c.DataDir, cookieFile))
	if err == nil {
		slog.Info("Loaded session cookie, skipping login")

		c.noLogin = true

		return nil
	} else if !errors.Is(err, ErrSessionCookieExpired) && !errors.Is(err, ErrNoCookieFile) {
		return fmt.Errorf("failed to load cookies: %w", err)
	}

	c.noLogin = false

	slog.Info("Navigating to Last.fm login page", "url", lastFMLoginURL)

	timeoutCtx, cancel := context.WithTimeout(ctx, browserOperationsTimeout)
	defer cancel()

	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate(lastFMLoginURL),
		chromedp.ActionFunc(clickConsentBanner),
		chromedp.SendKeys(`id_username_or_email`, strings.ToLower(c.LastFMUsername), chromedp.ByID),
		chromedp.SendKeys(`id_password`, c.LastFMPassword, chromedp.ByID),
		chromedp.Click(`//div[@class='form-submit']/button[@class='btn-primary']`, chromedp.BySearch),
		chromedp.WaitVisible(`//h1[@class='header-title']/a`, chromedp.BySearch),
	)
	if err != nil {
		return fmt.Errorf("failed to login to Last.fm: %w", err)
	}

	// Save cookies for reuse
	if err := saveCookies(timeoutCtx, cookieFile, c.DataDir); err != nil {
		slog.Warn("Could not save cookies", "err", err)
	} else {
		slog.Info("Saved login cookies to " + cookieFile)
	}

	slog.Info("Successfully logged in!")

	return nil
}

func getCookies(ctx context.Context) ([]*network.Cookie, error) {
	var cookies []*network.Cookie

	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error

		cookies, err = network.GetCookies().Do(ctx)

		return err
	}))
	if err != nil {
		return nil, err
	}

	return cookies, nil
}

// Save cookies after login
func saveCookies(ctx context.Context, filename string, dataDir string) error {
	cookies, err := getCookies(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cookies: %w", err)
	}

	f, err := os.Create(path.Join(dataDir, filename))
	if err != nil {
		return fmt.Errorf("failed to save cookie file: %w", err)
	}
	defer CloseFile(f)

	return json.NewEncoder(f).Encode(cookies)
}

var ErrSessionCookieExpired = errors.New("cookie expired")
var ErrNoCookieFile = errors.New("no cookie file")

func loadCookies(ctx context.Context, filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNoCookieFile
		}

		return err
	}
	defer CloseFile(f)

	var cookies []*network.Cookie
	if err := json.NewDecoder(f).Decode(&cookies); err != nil {
		return err
	}

	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, cookie := range cookies {
			cookieExpiry := cdp.TimeSinceEpoch(time.Unix(int64(cookie.Expires), 0))
			if cookie.Name == "sessionid" {
				if cookieExpiry.Time().Before(time.Now()) && cookie.Name == "sessionid" {
					slog.Info("Session cookie expired, forcing login")
					return ErrSessionCookieExpired
				}
			}

			err := network.SetCookie(cookie.Name, cookie.Value).
				WithDomain(cookie.Domain).
				WithPath(cookie.Path).
				WithHTTPOnly(cookie.HTTPOnly).
				WithSecure(cookie.Secure).
				WithExpires(&cookieExpiry).
				Do(ctx)
			if err != nil {
				return err
			}
		}

		return nil
	}))
}
