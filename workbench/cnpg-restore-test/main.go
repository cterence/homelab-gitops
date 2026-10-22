// cnpg-restore-test verifies that CNPG cluster backups can be restored
// by creating temporary restore clusters, checking database sizes, and
// cleaning up. It runs as a Kubernetes CronJob.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "cnpg-restore-test: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg := Config{
		Namespace:               "cnpg-restore-test",
		Concurrency:             0, // 0 = auto-detect
		CapacityMargin:          1.5,
		CapacityMountpointRegex: "/mnt/mx500-0.",
		PrometheusURL:           "http://prometheus:9090",
		OTeleEndpoint:           "opentelemetry-collector:4317",
	}

	var concurrencyStr string

	cmd := &cli.Command{
		Name:  "cnpg-restore-test",
		Usage: "verify that CNPG cluster backups can be restored",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "dry-run", Value: true, Destination: &cfg.DryRun, Usage: "discover and capacity check only, no resource creation (use --dry-run=false to run full restore)"},
			&cli.StringFlag{Name: "namespace", Value: cfg.Namespace, Destination: &cfg.Namespace, Usage: "target namespace for restore clusters"},
			&cli.StringFlag{Name: "concurrency", Value: "auto", Destination: &concurrencyStr, Usage: `max concurrent cluster restores ("auto" to auto-detect from capacity)`},
			&cli.FloatFlag{Name: "capacity-margin", Value: cfg.CapacityMargin, Destination: &cfg.CapacityMargin, Usage: "safety multiplier for disk capacity check"},
			&cli.StringFlag{Name: "capacity-mountpoint-regex", Value: cfg.CapacityMountpointRegex, Destination: &cfg.CapacityMountpointRegex, Usage: "Prometheus mountpoint regex for capacity check (min across matching nodes)"},
			&cli.StringFlag{Name: "prometheus-endpoint", Value: cfg.PrometheusURL, Destination: &cfg.PrometheusURL, Usage: "Prometheus API URL for capacity queries"},
			&cli.StringFlag{Name: "otel-endpoint", Value: cfg.OTeleEndpoint, Destination: &cfg.OTeleEndpoint, Usage: "OTLP gRPC endpoint for pushing metrics"},
			&cli.StringFlag{Name: "kubeconfig", Destination: &cfg.KubeconfigPath, Usage: "path to kubeconfig (defaults to ~/.kube/config or in-cluster)"},
			&cli.StringFlag{Name: "filter", Destination: &cfg.ClusterFilter, Usage: "comma-separated glob patterns to filter clusters by namespace/name (e.g. 'pocket-id/*,vaultwarden/*')"},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			return runRestore(ctx, cfg, concurrencyStr)
		},
	}

	return cmd.Run(ctx, args)
}

// runRestore is the command body: everything after flag parsing.
func runRestore(ctx context.Context, cfg Config, concurrencyStr string) error {
	// Parse concurrency: "auto" = 0 (auto-detect), or a positive integer
	if concurrencyStr != "auto" {
		n, err := strconv.Atoi(concurrencyStr)
		if err != nil || n < 1 {
			return fmt.Errorf("invalid --concurrency value %q: use \"auto\" or a positive integer", concurrencyStr)
		}

		cfg.Concurrency = n
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	client, err := NewClient(cfg)
	if err != nil {
		return err
	}

	slog.Info("client initialized", "dry-run", cfg.DryRun, "namespace", cfg.Namespace, "concurrency", concurrencyStr)

	runStart := time.Now()

	// Verify the restore storage class exists and uses Delete reclaim policy
	sc, err := client.core.StorageV1().StorageClasses().Get(ctx, "cnpg-restore-test", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting storage class cnpg-restore-test: %w", err)
	}

	if sc.ReclaimPolicy == nil || *sc.ReclaimPolicy != "Delete" {
		return fmt.Errorf("storage class cnpg-restore-test has reclaimPolicy %v, expected Delete", sc.ReclaimPolicy)
	}

	slog.Info("storage class verified", "name", "cnpg-restore-test", "reclaimPolicy", sc.ReclaimPolicy)

	clusters, err := client.DiscoverClusters(ctx, cfg)
	if err != nil {
		return err
	}

	if len(clusters) == 0 {
		slog.Info("no clusters to test")
		return nil
	}

	// Compute max concurrency (auto-detect or fixed)
	maxConc, err := computeMaxConcurrency(cfg, clusters)
	if err != nil {
		return err
	}

	if maxConc == 0 {
		slog.Warn("capacity check failed, aborting run")

		_ = pushMetrics(context.Background(), cfg, nil, time.Since(runStart), true)

		return errors.New("insufficient disk space")
	}

	cfg.Concurrency = maxConc
	slog.Info("using concurrency", "concurrency", cfg.Concurrency)

	if cfg.DryRun {
		slog.Info("dry-run complete, skipping restore")
		return nil
	}

	// From here on we create resources — register cleanup for any leftovers
	// (only runs if interrupted before per-cluster cleanup completes)
	allResults := make([]RestoreResult, len(clusters))
	cleanupRan := false

	cleanup := func() {
		if cleanupRan {
			return
		}

		cleanupRan = true

		slog.Info("starting cleanup (leftover resources)")

		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()

		vr := make([]VerifyResult, len(allResults))
		for i, r := range allResults {
			vr[i] = VerifyResult{RestoreResult: r}
		}

		client.Cleanup(cleanupCtx, cfg, vr)
	}
	defer cleanup()

	// Ensure namespace exists (secret is provisioned by the Helm chart)
	if err := client.ensureNamespace(ctx, cfg.Namespace); err != nil {
		return fmt.Errorf("ensuring namespace: %w", err)
	}

	// Process each cluster: restore → verify → cleanup, with bounded concurrency
	signalCtx := ctx
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)

	var (
		mu            sync.Mutex
		verifyResults []VerifyResult
	)

	for i, ci := range clusters {
		g.Go(func() error {
			// Restore
			rr := client.restoreOne(ctx, cfg.Namespace, ci)
			allResults[i] = rr

			if rr.Error != nil {
				slog.Error("restore failed", "cluster", rr.ClusterName, "error", rr.Error)
				// Cleanup this cluster immediately
				client.cleanupOne(ctx, cfg, VerifyResult{RestoreResult: rr})
				mu.Lock()

				verifyResults = append(verifyResults, VerifyResult{RestoreResult: rr, Error: rr.Error})
				mu.Unlock()

				return nil
			}

			// Verify
			vr := client.verifyOne(ctx, cfg, rr)

			mu.Lock()

			verifyResults = append(verifyResults, vr)
			mu.Unlock()

			if vr.Passed {
				slog.Info("verification passed", "cluster", rr.ClusterName)
			} else {
				slog.Warn("verification failed", "cluster", rr.ClusterName)
			}

			// Cleanup this cluster immediately
			client.cleanupOne(ctx, cfg, vr)

			return nil
		})
	}

	_ = g.Wait()

	// Per-cluster cleanup already ran in the pipeline. Skip the deferred
	// safety-net cleanup unless the run was interrupted, in which case
	// goroutines may not have fully cleaned up. The errgroup cancels its
	// derived context on Wait return regardless of success, so check the
	// parent signal context instead.
	if signalCtx.Err() == nil {
		cleanupRan = true
	}

	passed := 0
	failed := 0

	for _, vr := range verifyResults {
		if vr.Passed {
			passed++
		} else {
			failed++
		}
	}

	slog.Info("verification complete", "passed", passed, "failed", failed)

	runDuration := time.Since(runStart)
	if err := pushMetrics(context.Background(), cfg, verifyResults, runDuration, false); err != nil {
		slog.Error("failed to push metrics", "error", err)
	}

	if failed > 0 && passed == 0 {
		return fmt.Errorf("all %d clusters failed verification", failed)
	}

	return nil
}
