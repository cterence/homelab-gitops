package main

import (
	"encoding/json"
	"testing"

	dashboardv2 "github.com/grafana/grafana-foundation-sdk/go/dashboardv2"
	"github.com/grafana/grafana-foundation-sdk/go/resource"
)

// buildManifest renders a dashboard builder into the provisioned CRD wrapper.
func buildManifest(t *testing.T, b *dashboardv2.DashboardBuilder) map[string]any {
	t.Helper()

	spec, err := b.Build()
	if err != nil {
		t.Fatalf("building dashboard: %v", err)
	}

	manifest, err := resource.NewManifestBuilder().
		ApiVersion("dashboard.grafana.app/v2").
		Kind(resource.DashboardKind).
		Metadata(resource.NewMetadataBuilder().Name("test")).
		Spec(spec).
		Build()
	if err != nil {
		t.Fatalf("building manifest: %v", err)
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshaling manifest: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}

	return out
}

func TestDiskUsageManifest(t *testing.T) {
	m := buildManifest(t, diskUsage())
	spec := m["spec"].(map[string]any)
	elements := spec["elements"].(map[string]any)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"apiVersion", m["apiVersion"], "dashboard.grafana.app/v2"},
		{"kind", m["kind"], "Dashboard"},
		{"title", spec["title"], "Disk usage"},
		{"panel count", len(elements), 11},
		{"row count", len(spec["layout"].(map[string]any)["spec"].(map[string]any)["rows"].([]any)), 4},
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

func TestDiskUsagePanelQueries(t *testing.T) {
	m := buildManifest(t, diskUsage())
	elements := m["spec"].(map[string]any)["elements"].(map[string]any)

	tests := []struct {
		key   string
		title string
		expr  string
	}{
		{"panel-1", "Volume usage", "node_disk_usage_bytes"},
		{"panel-4", "Top 10 by absolute usage", "topk(10, node_disk_usage_bytes)"},
		{"panel-5", "Absolute capacity", `avg(kubelet_volume_stats_capacity_bytes{persistentvolumeclaim="arr-stack-data"})`},
		{"panel-2", "Absolute used space", `avg(kubelet_volume_stats_used_bytes{persistentvolumeclaim="arr-stack-data"})`},
		{"panel-3", "Relative used space", `1 - kubelet_volume_stats_available_bytes{persistentvolumeclaim="arr-stack-data"}/kubelet_volume_stats_capacity_bytes{persistentvolumeclaim="arr-stack-data"}`},
		{"panel-9", "Absolute capacity", `avg(kubelet_volume_stats_capacity_bytes{persistentvolumeclaim="storage-loki-0"})`},
		{"panel-10", "Absolute used space", `avg(kubelet_volume_stats_used_bytes{persistentvolumeclaim="storage-loki-0"})`},
		{"panel-11", "Relative used space", `1 - kubelet_volume_stats_available_bytes{persistentvolumeclaim="storage-loki-0"}/kubelet_volume_stats_capacity_bytes{persistentvolumeclaim="storage-loki-0"}`},
		{"panel-6", "Absolute capacity", `avg(kubelet_volume_stats_capacity_bytes{persistentvolumeclaim="storage-pool"})`},
		{"panel-7", "Absolute used space", `avg(kubelet_volume_stats_used_bytes{persistentvolumeclaim="storage-pool"})`},
		{"panel-8", "Relative used space", `1 - kubelet_volume_stats_available_bytes{persistentvolumeclaim="storage-pool"}/kubelet_volume_stats_capacity_bytes{persistentvolumeclaim="storage-pool"}`},
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

func TestDiskUsageTransformations(t *testing.T) {
	m := buildManifest(t, diskUsage())
	elements := m["spec"].(map[string]any)["elements"].(map[string]any)

	tests := []struct {
		key   string
		regex string
	}{
		{"panel-1", `\/data\/pvc-[\d\w\-]+_(.+)`},
		{"panel-4", `.+\/pvc-[\d\w\-]+_(.+)`},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			panel := elements[tt.key].(map[string]any)["spec"].(map[string]any)

			transformations := panel["data"].(map[string]any)["spec"].(map[string]any)["transformations"].([]any)
			if len(transformations) != 1 {
				t.Fatalf("transformation count: got %d, want 1", len(transformations))
			}

			tr := transformations[0].(map[string]any)
			if tr["group"] != "renameByRegex" {
				t.Errorf("group: got %v, want renameByRegex", tr["group"])
			}

			options := tr["spec"].(map[string]any)["options"].(map[string]any)
			if options["regex"] != tt.regex {
				t.Errorf("regex: got %v, want %v", options["regex"], tt.regex)
			}

			if options["renamePattern"] != "$1" {
				t.Errorf("renamePattern: got %v, want $1", options["renamePattern"])
			}
		})
	}
}
