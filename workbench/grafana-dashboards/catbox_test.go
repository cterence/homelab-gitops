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
		key    string
		title  string
		expr   string
		legend string
	}{
		{"panel-1", "Spool fill", "max(catbox_spool_bytes / catbox_spool_cap_bytes)", "fill"},
		{"panel-2", "Parked deliveries", "max(catbox_spool_items)", "parked"},
		{"panel-3", "Mesh members", "max(catbox_members)", "members"},
		{"panel-4", "Spool bytes", "max(catbox_spool_bytes)", "spool bytes"},
		{"panel-5", "Parked deliveries over time", "max(catbox_spool_items)", "parked"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			panel := elements[tt.key].(map[string]any)["spec"].(map[string]any)
			if panel["title"] != tt.title {
				t.Errorf("title: got %v, want %v", panel["title"], tt.title)
			}

			query := panel["data"].(map[string]any)["spec"].(map[string]any)["queries"].([]any)[0]

			querySpec := query.(map[string]any)["spec"].(map[string]any)["query"].(map[string]any)["spec"].(map[string]any)

			expr := querySpec["expr"]
			if expr != tt.expr {
				t.Errorf("expr: got %v, want %v", expr, tt.expr)
			}

			if querySpec["legendFormat"] != tt.legend {
				t.Errorf("legend: got %v, want %v", querySpec["legendFormat"], tt.legend)
			}
		})
	}
}

func TestCatboxSpoolFillDecimals(t *testing.T) {
	m := buildManifest(t, catbox())
	panel := m["spec"].(map[string]any)["elements"].(map[string]any)["panel-1"].(map[string]any)["spec"].(map[string]any)

	defaults := panel["vizConfig"].(map[string]any)["spec"].(map[string]any)["fieldConfig"].(map[string]any)["defaults"].(map[string]any)
	if defaults["decimals"] != float64(2) {
		t.Errorf("decimals: got %v, want 2", defaults["decimals"])
	}
}
