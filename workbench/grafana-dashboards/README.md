# grafana-dashboards

Generates Grafana dashboards as code with the
[Grafana Foundation SDK](https://grafana.com/docs/grafana/latest/as-code/observability-as-code/foundation-sdk/)
(v2 schema) and writes them into `k8s-apps/kube-prometheus-stack/dashboards/`.
The umbrella chart packages that directory into the `grafana-gitops-dashboards`
ConfigMap, which the Grafana pod mounts at the `gitops` dashboard provider path
(`allowUiUpdates: false` — the generated file always wins).

## Usage

```sh
go run . [-out <dir>]
```

`-out` defaults to `../../k8s-apps/kube-prometheus-stack/dashboards`.

## Adding a dashboard

1. Add a builder function returning `*dashboardv2.DashboardBuilder`.
2. Register it in the `dashboards` slice in `main.go` with a readable, stable
   UID (it becomes the dashboard URL slug) and a filename.
3. `go run .` and commit the generated JSON together with the code.

Output files are CRD-wrapped (`apiVersion: dashboard.grafana.app/v2`,
`kind: Dashboard`, `metadata.name` = UID), the format Grafana file
provisioning expects for v2 dashboards.

## Conventions

- Every query gets an explicit legend: `__auto` renders the raw PromQL
  string whenever the expression is wrapped (max, avg, ...), which is what
  the panel then displays.
- Apps running with `hostNetwork` and a per-pod PodMonitor overlap in
  Prometheus during rollouts (the old pod's target keeps answering on the
  same node address). Wrap their per-pod gauges in `max(...)` so panels
  don't render one series per overlapping target.
