package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	dashboardv2 "github.com/grafana/grafana-foundation-sdk/go/dashboardv2"
	"github.com/grafana/grafana-foundation-sdk/go/resource"
)

// dashboard describes one generated dashboard: its Grafana UID (kept stable
// so existing URLs survive), its output filename, and its builder.
type dashboard struct {
	uid      string
	filename string
	build    func() *dashboardv2.DashboardBuilder
}

var dashboards = []dashboard{
	{uid: "disk-usage", filename: "disk-usage.json", build: diskUsage},
	{uid: "smartctl", filename: "smartctl.json", build: smartctl},
}

func run(ctx context.Context, outDir string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	for _, d := range dashboards {
		if err := writeDashboard(d, outDir); err != nil {
			return err
		}

		logger.Info("wrote dashboard", "uid", d.uid, "path", filepath.Join(outDir, d.filename))
	}

	return nil
}

func writeDashboard(d dashboard, outDir string) error {
	spec, err := d.build().Build()
	if err != nil {
		return fmt.Errorf("building dashboard %s: %w", d.uid, err)
	}

	manifest, err := resource.NewManifestBuilder().
		ApiVersion("dashboard.grafana.app/v2").
		Kind(resource.DashboardKind).
		Metadata(resource.NewMetadataBuilder().Name(d.uid)).
		Spec(spec).
		Build()
	if err != nil {
		return fmt.Errorf("building manifest for %s: %w", d.uid, err)
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling dashboard %s: %w", d.uid, err)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(outDir, d.filename), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing dashboard %s: %w", d.uid, err)
	}

	return nil
}

func main() {
	outDir := flag.String("out", "../../k8s-apps/kube-prometheus-stack/dashboards", "output directory for dashboard JSON files")

	flag.Parse()

	if err := run(context.Background(), *outDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
