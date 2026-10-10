package main

import (
	"errors"
	"fmt"
	"os"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

// route maps one webhook path to a Mistral agent invocation.
type route struct {
	Path             string `yaml:"path"`
	AgentID          string `yaml:"agent_id"`
	ConversationName string `yaml:"conversation_name"`
	Prompt           string `yaml:"prompt"`
	// DedupKey is a dot-path into the payload JSON ("alerts.0.labels.alertname").
	DedupKey    string `yaml:"dedup_key"`
	DedupWindow string `yaml:"dedup_window"`

	promptTpl *template.Template
	window    time.Duration
}

type config struct {
	Routes []route `yaml:"routes"`
}

func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	if len(cfg.Routes) == 0 {
		return nil, errors.New("no routes configured")
	}

	seen := map[string]bool{}

	for i := range cfg.Routes {
		r := &cfg.Routes[i]
		if r.Path == "" || r.AgentID == "" || r.Prompt == "" {
			return nil, fmt.Errorf("route %d: path, agent_id and prompt are required", i)
		}

		tpl, err := template.New("prompt").Parse(r.Prompt)
		if err != nil {
			return nil, fmt.Errorf("route %s: parsing prompt template: %w", r.Path, err)
		}

		r.promptTpl = tpl
		if r.DedupWindow != "" {
			w, err := time.ParseDuration(r.DedupWindow)
			if err != nil {
				return nil, fmt.Errorf("route %s: invalid dedup_window %q: %w", r.Path, r.DedupWindow, err)
			}

			r.window = w
		}

		if seen[r.Path] {
			return nil, fmt.Errorf("duplicate route path %q", r.Path)
		}

		seen[r.Path] = true
	}

	return &cfg, nil
}
