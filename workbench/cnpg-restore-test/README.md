# cnpg-restore-test

Daily restore verification for CloudNativePG clusters. Restores all CNPG
clusters from their barman-cloud backups into a temporary namespace, verifies
database integrity by comparing database sizes against the source clusters,
cleans up all resources, and pushes the results as OTLP metrics.

## How it works

1. **Discovery** — Lists all CNPG clusters using the barman-cloud plugin
   method across all namespaces. Optionally filtered by glob patterns.

2. **Capacity check** — Queries Prometheus for available disk space and
   computes the maximum concurrency that fits. Auto-detection tries from
   the total cluster count down to 1, picking the highest that fits within
   the safety margin.

3. **Restore** — For each cluster (bounded by concurrency), creates an
   ObjectStore and a Cluster CR with `bootstrap.recovery` pointing to the
   source backup. Uses a dedicated StorageClass with `reclaimPolicy: Delete`
   so no PVs are orphaned.

4. **Verify** — Execs into the restored postgres pod, lists all application
   databases (excluding `postgres`, `template0`, `template1`), and compares
   their sizes against the source cluster. Passes if all databases are
   within 10% of the source.

5. **Cleanup** — Deletes the Cluster, PVCs, ObjectStore, and shared secret
   immediately after each cluster is verified. No resources are left behind.

6. **Metrics** — Pushes OTLP gauges to the OpenTelemetry collector, which
   exports them to Prometheus via its Prometheus exporter endpoint.

## Usage

```bash
# Dry run (default): discover clusters, check capacity, don't create resources
go run .

# Full run
go run . --dry-run=false

# Filter to specific clusters (comma-separated globs)
go run . --filter 'pocket-id/*,vaultwarden/*' --dry-run=false

# Fixed concurrency
go run . --concurrency 2 --dry-run=false

# Auto-detect concurrency (default)
go run . --concurrency auto --dry-run=false
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `true` | Discover and capacity check only. Use `--dry-run=false` to run full restore. |
| `--namespace` | `cnpg-restore-test` | Target namespace for restore clusters. |
| `--concurrency` | `auto` | Max concurrent restores. `auto` picks the highest that fits disk capacity. |
| `--capacity-margin` | `1.5` | Safety multiplier for disk capacity check. |
| `--filter` | (empty) | Comma-separated glob patterns to filter clusters by `namespace/name`. |
| `--prometheus-endpoint` | `http://prometheus:9090` | Prometheus API URL for capacity queries. |
| `--otel-endpoint` | `opentelemetry-collector:4317` | OTLP gRPC endpoint for pushing metrics. |
| `--kubeconfig` | `~/.kube/config` | Path to kubeconfig (auto-detects in-cluster). |

## Metrics

All metrics are gauges representing the latest run's values:

| Metric | Description |
|--------|-------------|
| `cnpg_restore_test_total` | Number of clusters tested |
| `cnpg_restore_test_success` | Clusters that passed verification |
| `cnpg_restore_test_failure` | Clusters that failed verification |
| `cnpg_restore_test_skipped` | Clusters skipped (no backup, etc.) |
| `cnpg_restore_test_capacity_skipped` | 1 if run was aborted due to insufficient capacity |
| `cnpg_restore_test_run_duration_seconds` | Total run duration in seconds |

## Deployment

The Helm chart at `k8s-apps/cnpg-restore-test/` deploys:
- A CronJob running daily at 04:00 UTC (after CNPG backups at 01:00 and Velero at 02:00)
- A StorageClass `cnpg-restore-test` with `reclaimPolicy: Delete`
- RBAC (ServiceAccount, ClusterRole, ClusterRoleBinding)
- PrometheusRule alerts (`CnpgRestoreTestFailed`, `CnpgRestoreTestStale`)

The image is built via Kaniko from `workbench/cnpg-restore-test/`.

## Concurrency and capacity

With `--concurrency auto` (default), the tool queries Prometheus for available
disk space, sorts clusters by storage size descending, and finds the highest N
where `sum(N largest cluster sizes) * margin <= available space`. This ensures
only N clusters exist simultaneously, each restored → verified → cleaned up
before the next batch starts.

With `--concurrency N`, the tool uses exactly N. If N clusters don't fit,
the run is aborted.

## Lease

A Kubernetes Lease (`coordination.k8s.io/v1`) ensures only one instance runs
at a time. The lease is acquired before any resources are created and released
on exit (including signals). Dry runs skip the lease.
