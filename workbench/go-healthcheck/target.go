package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/hellofresh/health-go/v5"
)

type Target interface {
	New(uri string) error
	Register(h *health.Health, c *Config) error
	String() string
}

// redactedURI masks credentials in an endpoint for error messages, whether
// or not the URI parses.
func redactedURI(uri string) string {
	if u, err := url.Parse(uri); err == nil {
		return u.Redacted()
	}

	if i := strings.LastIndex(uri, "@"); i >= 0 {
		return "xxx@" + uri[i+1:]
	}

	return uri
}

func Register(t Target, endpoint string, h *health.Health, c *Config) error {
	if err := t.New(endpoint); err != nil {
		return fmt.Errorf("failed to create target %s: %v", redactedURI(endpoint), err)
	}

	if err := t.Register(h, c); err != nil {
		return fmt.Errorf("failed to register health check %s: %v", t, err)
	}

	return nil
}
