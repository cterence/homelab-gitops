package main

import (
	"fmt"
	"net/url"
	"time"

	"github.com/hellofresh/health-go/v5"
	"github.com/hellofresh/health-go/v5/checks/postgres"
)

type PostgreSQL struct {
	URI *url.URL
}

func (t *PostgreSQL) New(uri string) error {
	u, err := url.ParseRequestURI(uri)
	if err != nil {
		return fmt.Errorf("failed to parse postgresql url %s: %v", u, err)
	}

	if u.Scheme != "postgresql" {
		return fmt.Errorf("invalid scheme for postgresql target: %s", u.Scheme)
	}

	t.URI = u

	return nil
}

func (t *PostgreSQL) Register(h *health.Health, c *Config) error {
	err := h.Register(health.Config{
		Name:      t.URI.Host,
		Timeout:   time.Second * time.Duration(c.Timeout),
		SkipOnErr: false,
		Check: postgres.New(postgres.Config{
			DSN: t.URI.String(),
		}),
	})
	if err != nil {
		return fmt.Errorf("failed to register postgresql health check %s: %v", t.URI, err)
	}

	return nil
}

func (t *PostgreSQL) String() string {
	return t.URI.Redacted()
}
