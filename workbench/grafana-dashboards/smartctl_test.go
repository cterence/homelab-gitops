package main

import (
	"testing"
)

func TestSmartctlManifest(t *testing.T) {
	m := buildManifest(t, smartctl())
	spec := m["spec"].(map[string]any)
	elements := spec["elements"].(map[string]any)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"apiVersion", m["apiVersion"], "dashboard.grafana.app/v2"},
		{"kind", m["kind"], "Dashboard"},
		{"title", spec["title"], "SMART health"},
		{"panel count", len(elements), 7},
		{"row count", len(spec["layout"].(map[string]any)["spec"].(map[string]any)["rows"].([]any)), 2},
		{"timezone", spec["timeSettings"].(map[string]any)["timezone"], "browser"},
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

func TestSmartctlPanelQueries(t *testing.T) {
	m := buildManifest(t, smartctl())
	elements := m["spec"].(map[string]any)["elements"].(map[string]any)

	tests := []struct {
		key   string
		title string
		expr  string
	}{
		{"panel-1", "SMART status", "smartctl_device_smart_status"},
		{"panel-2", "Temperature", "smartctl_device_temperature"},
		{"panel-3", "Reallocated sectors", `smartctl_device_attribute{attribute_name="Reallocated_Sector_Ct",attribute_value_type="raw"}`},
		{"panel-4", "Pending sectors", `smartctl_device_attribute{attribute_name="Current_Pending_Sector",attribute_value_type="raw"}`},
		{"panel-5", "Power on hours", `smartctl_device_attribute{attribute_name="Power_On_Hours",attribute_value_type="raw"}`},
		{"panel-6", "Temperature", "smartctl_device_temperature"},
		{"panel-7", "Sector errors", `smartctl_device_attribute{attribute_name="Reallocated_Sector_Ct",attribute_value_type="raw"}`},
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
