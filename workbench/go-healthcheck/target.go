package main

import (
	"fmt"

	"github.com/hellofresh/health-go/v5"
)

type Target interface {
	New(uri string) error
	Register(h *health.Health, c *Config) error
	String() string
}

func Register(t Target, endpoint string, h *health.Health, c *Config) error {
	if err := t.New(endpoint); err != nil {
		return fmt.Errorf("failed to create target %s: %v", endpoint, err)
	}

	if err := t.Register(h, c); err != nil {
		return fmt.Errorf("failed to register health check %s: %v", endpoint, err)
	}

	return nil
}
