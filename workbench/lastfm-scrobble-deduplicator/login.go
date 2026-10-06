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

	err = chromedp.Do(timeoutCtx,
		chromedp.Navigate(lastFMLoginURL),
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			return clickConsentBanner(ctx)
		}),
		chromedp.SendKeys(chromedp.ID(`id_username_or_email`), strings.ToLower(c.LastFMUsername)),
		chromedp.SendKeys(chromedp.ID(`id_password`), c.LastFMPassword),
		chromedp.Click(`//div[@class='form-submit']/button[@class='btn-primary']`),
		chromedp.WaitVisible(`//h1[@class='header-title']/a`),
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
	res, err := chromedp.Call(ctx, network.GetCookies, network.GetCookiesParams{})
	if err != nil {
		return nil, err
	}

	return res.Cookies, nil
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

	return chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		for _, cookie := range cookies {
			cookieExpiryTime := time.Unix(int64(cookie.Expires), 0)
			if cookie.Name == "sessionid" && cookieExpiryTime.Before(time.Now()) {
				slog.Info("Session cookie expired, forcing login")

				return ErrSessionCookieExpired
			}

			_, err := chromedp.Call(ctx, network.SetCookie, network.SetCookieParams{
				Name:     cookie.Name,
				Value:    cookie.Value,
				Domain:   cookie.Domain,
				Path:     cookie.Path,
				HTTPOnly: &cookie.HTTPOnly,
				Secure:   &cookie.Secure,
				Expires:  cdp.TimeSinceEpoch(cookieExpiryTime.Unix()),
			})
			if err != nil {
				return err
			}
		}

		return nil
	}))
}
