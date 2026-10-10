package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(*testing.T, *config)
	}{
		{
			name: "valid route",
			yaml: `routes:
  - path: /alerts
    agent_id: ag_123
    conversation_name: alert-triage
    prompt: |
      Alert fired: {{.JSON}}
    dedup_key: alerts.0.labels.alertname
    dedup_window: 15m
`,
			check: func(t *testing.T, cfg *config) {
				r := cfg.Routes[0]
				if r.Path != "/alerts" || r.AgentID != "ag_123" {
					t.Errorf("got route %+v", r)
				}

				if r.window.String() != "15m0s" {
					t.Errorf("window = %v, want 15m", r.window)
				}

				if r.promptTpl == nil {
					t.Error("prompt template not parsed")
				}
			},
		},
		{
			name:    "no routes",
			yaml:    `routes: []`,
			wantErr: true,
		},
		{
			name:    "missing agent_id",
			yaml:    "routes:\n  - path: /alerts\n    prompt: hi\n",
			wantErr: true,
		},
		{
			name:    "missing prompt",
			yaml:    "routes:\n  - path: /alerts\n    agent_id: ag\n",
			wantErr: true,
		},
		{
			name:    "bad template",
			yaml:    "routes:\n  - path: /a\n    agent_id: ag\n    prompt: '{{.JSON'\n",
			wantErr: true,
		},
		{
			name:    "bad dedup window",
			yaml:    "routes:\n  - path: /a\n    agent_id: ag\n    prompt: p\n    dedup_window: soon\n",
			wantErr: true,
		},
		{
			name:    "duplicate paths",
			yaml:    "routes:\n  - path: /a\n    agent_id: ag\n    prompt: p\n  - path: /a\n    agent_id: ag2\n    prompt: p\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadConfig(writeTemp(t, tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("loadConfig() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}
