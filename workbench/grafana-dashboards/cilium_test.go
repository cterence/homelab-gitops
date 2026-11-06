package main

import "testing"

func TestCiliumManifest(t *testing.T) {
	m := buildManifest(t, cilium())
	spec := m["spec"].(map[string]any)
	elements := spec["elements"].(map[string]any)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"title", spec["title"], "Cilium & Hubble"},
		{"panel count", len(elements), 13},
		{"row count", len(spec["layout"].(map[string]any)["spec"].(map[string]any)["rows"].([]any)), 4},
		{"refresh", spec["timeSettings"].(map[string]any)["autoRefresh"], "30s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestCiliumPanelQueries(t *testing.T) {
	m := buildManifest(t, cilium())
	elements := m["spec"].(map[string]any)["elements"].(map[string]any)

	tests := []struct {
		key   string
		title string
		expr  string
	}{
		{"panel-1", "Cilium version", `count by (version) (cilium_version)`},
		{"panel-3", "Unreachable nodes", `max(cilium_unreachable_nodes)`},
		{"panel-7", "Drop rate by reason", `sum by (reason) (rate(hubble_drop_total[5m]))`},
		{"panel-8", "Policy-denied drops by node", `sum by (node) (rate(hubble_drop_total{reason="POLICY_DENIED"}[5m]))`},
		{"panel-10", "Flows by verdict", `sum by (verdict) (rate(hubble_flows_processed_total[5m]))`},
		{"panel-13", "Pod CIDR pool usage", `sum(cilium_ip_addresses{family="ipv4"}) / sum(cilium_ipam_capacity)`},
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
