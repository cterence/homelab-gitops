# Agents instructions

## Modifications

For any modifications in this repo, ask the user whether to edit on the main branch without commiting or use git worktrees.
If using git worktrees:

- use the appropriate "using-git-worktrees" skill and place them in the .worktrees directory.
- create a PR using gh CLI when finished.

## Repo layout

```
argocd-apps/   ApplicationSet registering every deployed app
k8s-apps/      One Helm umbrella chart per app (<app>/Chart.yaml + values.yaml)
workbench/     Go source code + Dockerfiles for self-built images
scripts/       Helper scripts (deployed-apps table, pv-diff)
docs/superpowers/  Design specs and implementation plans
terraform/     Infrastructure provisioning (secrets, external-secrets)
```

## Deployed apps

Always check `./argocd-apps/applicationset.yaml` for the list of currently deployed apps.
Each entry maps to `k8s-apps/<name>/` as a Helm chart source.

## Superpowers

Never git add things in docs/superpowers forcefully.

## Versioning

When modifying code under `workbench/<app>/`, bump both the build tag and the image tag:

- `workbench/<app>/build.yaml` — increment `tag`
- `k8s-apps/<app>/values.yaml` — update the image `tag` to match

## Golang

### Build artifacts

When running `go build`, always delete the generated binary afterwards.

### Linting

Always run `golangci-lint run --fix ./...` before considering Go work done.
Fix all reported issues. If a lint rule seems wrong, discuss with the user rather than disabling it.

### Static analysis

Always run `staticcheck ./...` after linting. Fix all reported issues.

### Code conventions

- Flat `package main` layout — no subpackages, no `internal/` directory.
- Module path: `github.com/cterence/homelab-gitops/workbench/<app>`.
- Go version: 1.26 (set in `go.mod`).
- `main()` calls a `run() error` function; on error print to stderr and `os.Exit(1)`.
- Imports grouped in three blocks separated by blank lines: stdlib, third-party, local (local not needed yet since everything is `package main`).
- Error handling: wrap with context using `fmt.Errorf("doing X: %w", err)`. Never both log and return an error — pick one.
- Logging: use `log/slog` with a JSON handler (`slog.NewJSONHandler(os.Stdout, nil)`). Structured key-value pairs only.
- `context.Context` as first parameter on all methods that do I/O or are long-running.
- Concurrency: prefer `errgroup.WithContext` with `g.SetLimit()` for bounded parallelism.
- Graceful shutdown: use `signal.NotifyContext` for signal handling.

### Tests

- Test files live alongside source as `<name>_test.go` in the same `package main`.
- Table-driven tests using anonymous structs with `t.Run(tt.name, ...)`.
- Tests cover pure functions (parsing, comparison, logic) — no integration tests.
- Run tests with `go test ./...` from the app directory.
- Use `t.Setenv` for environment-dependent tests.

### Dockerfiles

- Multi-stage build: `golang:1.26-alpine` (or pinned by digest) as build stage, `scratch` or `gcr.io/distroless/static-debian12:nonroot` as final.
- Build flags: `CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w"`.
- No Makefiles. Build is entirely Dockerfile-based; Kaniko builds in-cluster.

## Helm charts (k8s-apps/)

- Each app directory is an umbrella chart: `Chart.yaml` + `values.yaml`.
- `Chart.yaml`: `apiVersion: v2`, `version: 0.1.0` (never bumped), dependencies on upstream charts.
- Most common dependency: `app-template` (bjw-s-labs).
- Workbench app images reference the in-cluster registry: `registry.terence.cloud/<app>` with a `tag` matching `build.yaml`.
- Third-party images are pinned by digest (`tag@sha256:...`).

## Commit messages

Format: `<app-name>: <description>` — lowercase, short, imperative.
Example: `cnpg-restore-test: fix rbac`

## Renovate

Config is at `/renovate.json5`. Auto-merge is enabled for non-major updates and digests.
Renovate uses the `fix:` conventional-commit prefix.

## CI

- `update-deployed-apps.yaml` — regenerates the deployed apps table in README.md on push to main.
- `pr-argo-diff.yaml` — runs `argocd app diff` per changed app on PRs touching `k8s-apps/**` and posts the diff as a PR comment.
- No Go CI pipeline — builds happen in-cluster via Kaniko.
