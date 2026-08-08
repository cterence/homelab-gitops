// cnpg-restore-test verifies that CNPG cluster backups can be restored
// by creating temporary restore clusters, checking database sizes, and
// cleaning up. It runs as a Kubernetes CronJob.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cnpg-restore-test: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := Config{
		Namespace:      "cnpg-restore-test",
		Concurrency:    0, // 0 = auto-detect
		CapacityMargin: 1.5,
		PrometheusURL:  "http://prometheus:9090",
		OTeleEndpoint:  "opentelemetry-collector:4317",
	}

	var concurrencyStr string

	flag.BoolVar(&cfg.DryRun, "dry-run", true, "discover and capacity check only, no resource creation (use --dry-run=false to run full restore)")
	flag.StringVar(&cfg.Namespace, "namespace", cfg.Namespace, "target namespace for restore clusters")
	flag.StringVar(&concurrencyStr, "concurrency", "auto", "max concurrent cluster restores (\"auto\" to auto-detect from capacity)")
	flag.Float64Var(&cfg.CapacityMargin, "capacity-margin", cfg.CapacityMargin, "safety multiplier for disk capacity check")
	flag.StringVar(&cfg.PrometheusURL, "prometheus-endpoint", cfg.PrometheusURL, "Prometheus API URL for capacity queries")
	flag.StringVar(&cfg.OTeleEndpoint, "otel-endpoint", cfg.OTeleEndpoint, "OTLP gRPC endpoint for pushing metrics")
	flag.StringVar(&cfg.KubeconfigPath, "kubeconfig", "", "path to kubeconfig (defaults to ~/.kube/config or in-cluster)")
	flag.StringVar(&cfg.ClusterFilter, "filter", "", "comma-separated glob patterns to filter clusters by namespace/name (e.g. 'pocket-id/*,vaultwarden/*')")
	flag.Parse()

	// Parse concurrency: "auto" = 0 (auto-detect), or a positive integer
	if concurrencyStr != "auto" {
		n, err := fmt.Sscanf(concurrencyStr, "%d", &cfg.Concurrency)
		if err != nil || n != 1 || cfg.Concurrency < 1 {
			return fmt.Errorf("invalid --concurrency value %q: use \"auto\" or a positive integer", concurrencyStr)
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	client, err := NewClient(cfg)
	if err != nil {
		return err
	}

	slog.Info("client initialized", "dry-run", cfg.DryRun, "namespace", cfg.Namespace, "concurrency", concurrencyStr)

	signalCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ctx := signalCtx
	runStart := time.Now()

	// Acquire a lease so only one instance runs at a time (skip in dry-run)
	if !cfg.DryRun {
		identity := fmt.Sprintf("cnpg-restore-test-%d", time.Now().UnixNano())

		releaseLease, err := client.acquireLease(ctx, cfg.Namespace, "cnpg-restore-test", identity)
		if err != nil {
			return fmt.Errorf("acquiring lease: %w", err)
		}
		defer releaseLease()
	}

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

		cleanupErrs := client.Cleanup(cleanupCtx, cfg, vr)
		for _, ce := range cleanupErrs {
			slog.Error("cleanup error", "cluster", ce.ClusterName, "error", ce.Error)
		}
	}
	defer cleanup()

	// Ensure namespace exists (secret is provisioned by the Helm chart)
	if err := client.ensureNamespace(ctx, cfg.Namespace); err != nil {
		return fmt.Errorf("ensuring namespace: %w", err)
	}

	// Process each cluster: restore → verify → cleanup, with bounded concurrency
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)

	var (
		mu            sync.Mutex
		verifyResults []VerifyResult
	)

	for i, ci := range clusters {
		g.Go(func() error {
			// Restore
			rr, _ := client.restoreOne(ctx, cfg.Namespace, ci)
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
