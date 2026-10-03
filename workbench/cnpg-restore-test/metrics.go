package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
)

// pushMetrics pushes the run results as OTLP metrics to the OTel collector.
func pushMetrics(ctx context.Context, cfg Config, verifyResults []VerifyResult, runDuration time.Duration, capacitySkipped bool) error {
	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.OTeleEndpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return fmt.Errorf("creating OTLP exporter: %w", err)
	}

	provider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(exporter)),
	)

	meter := provider.Meter("cnpg-restore-test")

	total, success, failure, skipped := 0, 0, 0, 0
	for _, vr := range verifyResults {
		total++

		if vr.Error != nil {
			if vr.PodName == "" {
				skipped++
			} else {
				failure++
			}
		} else if vr.Passed {
			success++
		} else {
			failure++
		}
	}

	// Use gauges instead of counters — each run is a short-lived process,
	// so counters reset to 0 on every run and fluctuate in Prometheus.
	// Gauges represent the latest run's values, which is what we want.
	totalGauge, err := meter.Int64Gauge("cnpg_restore_test_total")
	if err != nil {
		return fmt.Errorf("creating total gauge: %w", err)
	}

	successGauge, _ := meter.Int64Gauge("cnpg_restore_test_success")
	failureGauge, _ := meter.Int64Gauge("cnpg_restore_test_failure")
	skippedGauge, _ := meter.Int64Gauge("cnpg_restore_test_skipped")
	capacitySkippedGauge, _ := meter.Int64Gauge("cnpg_restore_test_capacity_skipped")
	durationGauge, _ := meter.Int64Gauge("cnpg_restore_test_run_duration_seconds")

	totalGauge.Record(ctx, int64(total))
	successGauge.Record(ctx, int64(success))
	failureGauge.Record(ctx, int64(failure))
	skippedGauge.Record(ctx, int64(skipped))

	if capacitySkipped {
		capacitySkippedGauge.Record(ctx, 1)
	} else {
		capacitySkippedGauge.Record(ctx, 0)
	}

	durationGauge.Record(ctx, int64(runDuration.Seconds()))

	// Force flush metrics to the collector before shutting down.
	// Use a fresh context — the caller's ctx may be cancelled (signal).
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer flushCancel()

	if err := provider.ForceFlush(flushCtx); err != nil {
		slog.Warn("force flush failed", "error", err)
	}

	if err := provider.Shutdown(flushCtx); err != nil {
		slog.Warn("provider shutdown failed", "error", err)
	}

	slog.Info("metrics pushed", "total", total, "success", success, "failure", failure, "skipped", skipped)

	return nil
}
