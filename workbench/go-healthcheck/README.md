# go-healthcheck

Web service that aggregates health checks for HTTP endpoints, PostgreSQL
databases, and Redis instances into a single JSON status endpoint.

Migrated from https://github.com/cterence/go-healthcheck.

## How it works

1. **Config** — reads `config.yaml` from the working directory (`/app` in
   the container). Declares a name/version, a check timeout, and lists of
   targets per type.
2. **Targets** — each target is validated and registered with the
   hellofresh/health-go library. HTTP targets optionally use a client
   certificate and a configurable status-code error threshold; PostgreSQL
   and Redis targets connect with the given DSN.
3. **Serve** — a chi router serves `/` with the aggregated measurement
   (HTTP 200 when everything passes, 503 when any check fails) and `/health`
   as a liveness heartbeat.

## Configuration

```yaml
name: homelab
version: 1.0
timeout: 5
httpClientCertPath: /etc/ssl/private/tls.crt   # optional
httpClientKeyPath: /etc/ssl/private/tls.key    # optional
httpStatusCodeErrorThreshold: 400             # optional
targets:
  http:
    - https://example.com/health
  postgresql:
    - postgresql://user:pass@host:5432/db
  redis:
    - redis://host:6379
```

## Development

```bash
go build -o go-healthcheck .
go test ./...
```

Delete the built binary afterwards.

## Deployment

Consumed by `k8s-apps/go-healthcheck` as a Deployment behind the
`health.terence.cloud` ingress, image `registry.terence.cloud/go-healthcheck:<tag>`
matching `build.yaml`. Bump both tags together on code changes. The chart
generates `config.yaml` from the `urls` and `cnpgClusters` values and mounts
it at `/app/config.yaml`.
