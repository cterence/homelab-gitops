package main

import (
	"testing"
)

func TestTrivyManifest(t *testing.T) {
	m := buildManifest(t, trivy())
	spec := m["spec"].(map[string]any)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"apiVersion", m["apiVersion"], "dashboard.grafana.app/v2"},
		{"kind", m["kind"], "Dashboard"},
		{"title", spec["title"], "Trivy security"},
		{"panel count", len(spec["elements"].(map[string]any)), 9},
		{"row count", len(spec["layout"].(map[string]any)["spec"].(map[string]any)["rows"].([]any)), 3},
		{"timezone", spec["timeSettings"].(map[string]any)["timezone"], "browser"},
		{"refresh", spec["timeSettings"].(map[string]any)["autoRefresh"], "5m"},
		{"from", spec["timeSettings"].(map[string]any)["from"], "now-7d"},
		{"variable count", len(spec["variables"].([]any)), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestTrivyTopWorkloadsTableFormat(t *testing.T) {
	m := buildManifest(t, trivy())
	elements := m["spec"].(map[string]any)["elements"].(map[string]any)
	panel := elements["panel-8"].(map[string]any)["spec"].(map[string]any)
	queries := panel["data"].(map[string]any)["spec"].(map[string]any)["queries"].([]any)
	query := queries[0].(map[string]any)["spec"].(map[string]any)["query"].(map[string]any)
	spec := query["spec"].(map[string]any)

	if spec["format"] != "table" {
		t.Errorf("format: got %v, want table", spec["format"])
	}

	if spec["instant"] != true {
		t.Errorf("instant: got %v, want true", spec["instant"])
	}
}

func TestTrivyVariable(t *testing.T) {
	m := buildManifest(t, trivy())
	variable := m["spec"].(map[string]any)["variables"].([]any)[0].(map[string]any)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"kind", variable["kind"], "QueryVariable"},
		{"name", variable["spec"].(map[string]any)["name"], "namespace"},
		{"includeAll", variable["spec"].(map[string]any)["includeAll"], true},
		{"allValue", variable["spec"].(map[string]any)["allValue"], ".*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}

	query := variable["spec"].(map[string]any)["query"].(map[string]any)

	expr := query["spec"].(map[string]any)["expr"]
	if expr != "label_values(trivy_image_vulnerabilities, namespace)" {
		t.Errorf("expr: got %v", expr)
	}
}

func TestTrivyPanelQueries(t *testing.T) {
	m := buildManifest(t, trivy())
	elements := m["spec"].(map[string]any)["elements"].(map[string]any)

	tests := []struct {
		key   string
		title string
		expr  string
	}{
		{"panel-1", "Critical", `sum(trivy_image_vulnerabilities{namespace=~"$namespace",severity="Critical"})`},
		{"panel-2", "High", `sum(trivy_image_vulnerabilities{namespace=~"$namespace",severity="High"})`},
		{"panel-3", "Images with critical or high", `count(count by (image_digest) (trivy_image_vulnerabilities{namespace=~"$namespace",severity=~"Critical|High"}))`},
		{"panel-4", "Exposed secrets", `sum(trivy_image_exposedsecrets{namespace=~"$namespace"})`},
		{"panel-5", "Vulnerabilities by severity", `sum by (severity) (trivy_image_vulnerabilities{namespace=~"$namespace"})`},
		{"panel-6", "Config audit findings by severity", `sum by (severity) (trivy_resource_configaudits{namespace=~"$namespace"})`},
		{"panel-7", "Top 10 images by critical or high", `topk(10, sum by (namespace, image_repository, image_tag) (trivy_image_vulnerabilities{namespace=~"$namespace",severity=~"Critical|High"}))`},
		{"panel-8", "Top 15 workloads by criticals", `topk(15, sum by (namespace, resource_name, image_repository, image_tag) (trivy_image_vulnerabilities{namespace=~"$namespace",severity="Critical"}))`},
		{"panel-9", "Criticals by namespace", `topk(15, sum by (namespace) (trivy_image_vulnerabilities{namespace=~"$namespace",severity="Critical"}))`},
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
