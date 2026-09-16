package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/hellofresh/health-go/v5"
)

type HTTP struct {
	URL *url.URL
}

func (t *HTTP) New(uri string) error {
	u, err := url.ParseRequestURI(uri)
	if err != nil {
		return fmt.Errorf("failed to parse url %s: %v", u, err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid scheme for http target: %s", u.Scheme)
	}

	t.URL = u

	return nil
}

func (t *HTTP) Register(h *health.Health, c *Config) error {
	httpConfig := httpCustomConfig{
		URL:                          t.URL.String(),
		RequestTimeout:               time.Second * time.Duration(c.Timeout),
		HTTPStatusCodeErrorThreshold: c.HTTPStatusCodeErrorThreshold,
	}

	if c.HTTPClientCertPath != "" && c.HTTPClientKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(c.HTTPClientCertPath, c.HTTPClientKeyPath)
		if err != nil {
			return fmt.Errorf("failed to load client cert and key: %v", err)
		}

		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{cert},
				},
			},
			Timeout: time.Second * time.Duration(c.Timeout),
		}
		httpConfig.Client = client
	}

	if err := h.Register(health.Config{
		Name:      t.URL.Host,
		Timeout:   time.Second * time.Duration(c.Timeout),
		SkipOnErr: false,
		Check:     newHTTPCustomCheck(httpConfig),
	}); err != nil {
		return fmt.Errorf("failed to register HTTP health check %s: %v", t.URL, err)
	}

	return nil
}

func (t *HTTP) String() string {
	return t.URL.Redacted()
}

// Custom HTTP health check implementation to allow for client certs
// Original code from: https://github.com/hellofresh/health-go/blob/67b61702d81cada97e04c0b41d79115d8df57fde/checks/http/check.go

type httpCustomConfig struct {
	URL                          string
	RequestTimeout               time.Duration
	HTTPStatusCodeErrorThreshold int
	Client                       *http.Client
}

func newHTTPCustomCheck(config httpCustomConfig) func(ctx context.Context) error {
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 5 * time.Second
	}

	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}

	return func(ctx context.Context) error {
		req, err := http.NewRequest(http.MethodGet, config.URL, nil)
		if err != nil {
			return fmt.Errorf("creating the request for the health check failed: %w", err)
		}

		ctx, cancel := context.WithTimeout(ctx, config.RequestTimeout)
		defer cancel()

		req.Header.Set("Connection", "close")
		req = req.WithContext(ctx)

		res, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("making the request for the health check failed: %w", err)
		}
		defer bodyCloser(res.Body)

		threshold := config.HTTPStatusCodeErrorThreshold
		if threshold == 0 {
			threshold = http.StatusInternalServerError
		}

		if res.StatusCode >= threshold {
			return fmt.Errorf("remote service is not available at the moment: %d", res.StatusCode)
		}

		return nil
	}
}

func bodyCloser(body io.ReadCloser) {
	err := body.Close()
	if err != nil {
		fmt.Printf("failed to close response body: %v", err)
	}
}
