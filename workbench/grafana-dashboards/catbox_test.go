package main

import "testing"

func TestCatboxManifest(t *testing.T) {
	m := buildManifest(t, catbox())
	spec := m["spec"].(map[string]any)
	elements := spec["elements"].(map[string]any)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"apiVersion", m["apiVersion"], "dashboard.grafana.app/v2"},
		{"kind", m["kind"], "Dashboard"},
		{"title", spec["title"], "Catbox"},
		{"panel count", len(elements), 5},
		{"row count", len(spec["layout"].(map[string]any)["spec"].(map[string]any)["rows"].([]any)), 2},
		{"refresh", spec["timeSettings"].(map[string]any)["autoRefresh"], "30s"},
		{"variable count", len(spec["variables"].([]any)), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestCatboxPanelQueries(t *testing.T) {
	m := buildManifest(t, catbox())
	elements := m["spec"].(map[string]any)["elements"].(map[string]any)

	tests := []struct {
		key   string
		title string
		expr  string
	}{
		{"panel-1", "Spool fill", "catbox_spool_bytes / catbox_spool_cap_bytes"},
		{"panel-2", "Parked deliveries", "catbox_spool_items"},
		{"panel-3", "Mesh members", "catbox_members"},
		{"panel-4", "Spool bytes", "catbox_spool_bytes"},
		{"panel-5", "Parked deliveries over time", "catbox_spool_items"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			panel := elements[tt.key].(map[string]any)["spec"].(map[string]any)
			if panel["title"] != tt.title {
				t.Errorf("title: got %v, want %v", panel["title"], tt.title)
			}

			query := panel["data"].(map[string]any)["spec"].(map[string]any)["queries"].([]any)[0]

			expr := query.(map[string]any)["spec"].(map[string]any)["query"].(map[string]any)["spec"].(map[string]any)["expr"]
			if expr != tt.expr {
				t.Errorf("expr: got %v, want %v", expr, tt.expr)
			}
		})
	}
}
