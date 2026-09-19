# Agents instructions

## Modifications

For any modifications in this repo, ask the user whether to edit on the main branch without commiting or use git worktrees.
If using git worktrees:

- use the appropriate "using-git-worktrees" skill and place them in the .worktrees directory.
- create a PR using gh CLI when finished.

## Development environment

The root `flake.nix` provides the tool shell and pre-commit checks for the workbench apps.

- `nix develop` — tool shell (go 1.26, golangci-lint, gotools, gitleaks, uv). Entering it installs the prek hooks into `.git/hooks/pre-commit`.
- Hooks run at commit time: gitleaks (secrets), gofmt (all `workbench/**.go`), and per-app `golangci-lint run` + `go vet ./...` for each Go module under `workbench/`. Hook entries are absolute nix store paths, so they also work from environments without the dev shell on PATH (e.g. VS Code git).
- `nix flake check` — runs the full hook set over the repo; run it before pushing.
- After changing hooks in `flake.nix`, re-enter `nix develop` once to refresh the installed config.
- Staticcheck runs through golangci-lint's `staticcheck` linter (the standalone package is gone from nixpkgs).
- The Go hooks need network access to download modules; they work on macOS (relaxed sandbox) and at commit time, but would fail on a strictly-sandboxed Linux CI.

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

An app is deployed if and only if `k8s-apps/<name>/appset.yaml` exists (the
ApplicationSet git generator matches `k8s-apps/*/appset.yaml`). That file also
carries per-app flags: `namespace`, `serverSideApply`, `serverSideDiff`,
`scaleToZero`. Undeployed charts live in `k8s-apps/archive/`.

Tool/API documentation for workbench apps lives in each app's `README.md` — update it whenever an app's interface (tool arguments, endpoints, env vars) changes.

## Superpowers

Never git add things in docs/superpowers forcefully.

## Versioning

When modifying code under `workbench/<app>/`, bump both the build tag and the image tag:

- `workbench/<app>/build.yaml` — increment `tag`
- `k8s-apps/<app>/values.yaml` — update the image `tag` to match. For images
  consumed as sidecars by other apps (rangemusique, apachewebdav,
  gitea-mirror-sync), update the tag in the consuming app's values.yaml
  instead — CI's tag-bump finds all references automatically.

Both bumps must land in the same commit/PR. The pipeline is:

1. On merge, the `build-<app>` ArgoCD application runs a Kaniko build in-cluster.
2. Kaniko pushes `registry.terence.cloud/<app>:<tag>` to the in-cluster registry.
3. ArgoCD syncs `k8s-apps/<app>` to the new tag and rolls the Deployment.

Caveats:

- Never reuse an old tag for new code — `pullPolicy: IfNotPresent` means nodes
  will keep serving the cached image. Always increment.
- After merging, check the `build-<app>` application in ArgoCD: if the Kaniko
  build fails, the app deployment will be stuck in ImagePullBackOff.
- `Chart.yaml` version stays `0.1.0` (never bumped) — the image tag is the
  release mechanism.

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
- `workbench-ci.yaml` — on PRs touching `workbench/**`: runs `go vet`/`go test`/golangci-lint per changed Go app, pytest per changed Python app, and a no-push docker build mirroring the Kaniko context. Its `tag-bump` job auto-bumps `build.yaml` (and the matching `k8s-apps` image tag) when a PR modifies a workbench app without bumping its tag; idempotent per PR (merge-base diff), works on Renovate PRs too.
- Image builds still happen in-cluster via Kaniko on merge to main; CI builds are validation only.
