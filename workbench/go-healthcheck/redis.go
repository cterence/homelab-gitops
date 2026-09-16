package main

import (
	"fmt"
	"net/url"
	"time"

	"github.com/hellofresh/health-go/v5"
	"github.com/hellofresh/health-go/v5/checks/redis"
)

type Redis struct {
	URI *url.URL
}

func (t *Redis) New(uri string) error {
	u, err := url.ParseRequestURI(uri)
	if err != nil {
		return fmt.Errorf("failed to parse redis url %s: %v", u, err)
	}

	if u.Scheme != "redis" && u.Scheme != "rediss" {
		return fmt.Errorf("invalid scheme for redis target: %s", u.Scheme)
	}

	t.URI = u

	return nil
}

func (t *Redis) Register(h *health.Health, c *Config) error {
	err := h.Register(health.Config{
		Name:      t.URI.Host,
		Timeout:   time.Second * time.Duration(c.Timeout),
		SkipOnErr: false,
		Check: redis.New(redis.Config{
			DSN: t.URI.String(),
		}),
	})
	if err != nil {
		return fmt.Errorf("failed to register redis health check %s: %v", t.URI, err)
	}

	return nil
}

func (t *Redis) String() string {
	return t.URI.Redacted()
}
