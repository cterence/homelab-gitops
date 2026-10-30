# Agents instructions

## Modifications

For any modifications in this repo, ask the user whether to edit on the main branch without commiting or use git worktrees.
If using git worktrees:

- use the appropriate "using-git-worktrees" skill and place them in the .worktrees directory.
- create a PR using gh CLI when finished.

## Development environment

The root `flake.nix` provides the tool shell and pre-commit checks for the workbench apps.

- `nix develop` — tool shell (go 1.27, golangci-lint, gotools, gitleaks, uv). Entering it installs the prek hooks into `.git/hooks/pre-commit`.
- Hooks run at commit time: gitleaks (secrets), gofmt (all `workbench/**.go`), and per-app `golangci-lint run` + `go vet ./...` for each Go module under `workbench/`. Hook entries are absolute nix store paths, so they also work from environments without the dev shell on PATH (e.g. VS Code git).
- `nix flake check` — runs the full hook set over the repo; run it before pushing.
- After changing hooks in `flake.nix`, re-enter `nix develop` once to refresh the installed config.
- Staticcheck runs through golangci-lint's `staticcheck` linter (the standalone package is gone from nixpkgs).
- The Go hooks need network access to download modules; they work on macOS (relaxed sandbox) and at commit time, but would fail on a strictly-sandboxed Linux CI.

## Repo layout

```
argocd-apps/   ApplicationSet registering every deployed app
k8s-apps/      One Helm umbrella chart per app (<app>/Chart.yaml + values.yaml)
charts/        Shared Helm charts rendered as extra Application sources
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

New apps pulling from `registry.terence.cloud` need two things, both easy to
forget (missing either leaves the app in ImagePullBackOff): their namespace in
the `registry-pull-secret` ClusterExternalSecret's `spec.namespaces` in
`k8s-apps/registry/values.yaml` — the registry app distributes its own pull
secret — and `imagePullSecrets: [{name: registry-pull-secret}]` under
`app-template.defaultPodOptions` in the app's own values.yaml.

Tool/API documentation for workbench apps lives in each app's `README.md` — update it whenever an app's interface (tool arguments, endpoints, env vars) changes.

## Grafana dashboards

Dashboards are code: builders live in `workbench/grafana-dashboards/` (one
file per dashboard, registered in `main.go`). Add a builder, run `go run .`
from that directory, and commit the regenerated JSON in
`k8s-apps/kube-prometheus-stack/dashboards/` together with the code. That
app's README has the full workflow.

## Superpowers

Never git add things in docs/superpowers forcefully.

## Versioning

Never bump image tags in PRs. Tags are bumped by CI on main after merge:

1. A PR changes `workbench/<app>/` code; the image tag is not touched.
2. On merge, the `workbench-tag-bump` workflow diffs the merge against the
   previous tip and, for each changed app without a tag bump, commits
   `<app>: bump image tag to <n>` on main: `workbench/<app>/build.yaml` plus
   the image tag in the consuming `k8s-apps/<app>/values.yaml`. Sidecar
   consumers (rangemusique, apachewebdav, gitea-mirror-sync) are updated in
   the consuming app's values.yaml — the bump script finds all references.
3. The bump commit makes the `build-<app>` ArgoCD application run a Kaniko
   build in-cluster, pushing `registry.terence.cloud/<app>:<tag>` to the
   in-cluster registry.
4. ArgoCD syncs `k8s-apps/<app>` to the new tag and rolls the Deployment.

Caveats:

- Never reuse an old tag for new code — `pullPolicy: IfNotPresent` means nodes
  will keep serving the cached image. Always increment.
- Every app carries the `charts/image-wait` chart as a second Application
  source (wired in `argocd-apps/applicationset.yaml`); it renders PreSync hooks
  that hold the sync until the workbench image tag exists in the registry, so
  rollouts never hit ImagePullBackOff while the Kaniko build is still running.
- After merging, check the `build-<app>` application in ArgoCD: if the Kaniko
  build fails, the consuming app's sync fails at the hook and the old pods keep
  running.
- Kaniko rebuilds only happen when the tag changes: the Job name embeds the
  tag and completed Jobs are kept (ArgoCD prunes the old-tag Job on the next
  bump), so no same-tag rebuild ever re-runs.
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
- Go version: 1.27.1 (set in `go.mod`).
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

- Multi-stage build: `golang:1.27-alpine` (or pinned by digest) as build stage, `scratch` or `gcr.io/distroless/static-debian12:nonroot` as final.
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
- `nix-cache.yaml` — builds every flake package per system and pushes to the niks3 binary cache on push to main touching the flake or `workbench/catbox/**`. On PRs, the `catbox-pr` job runs on every PR and builds `.#catbox`, `.#catbox-android-bin` and `.#catbox-android` (pushing them to the cache) when the PR touches catbox's Go module, the nix/android trees, the flake, or the workflow — this is the only place the go toolchain, vendorHash, and gradle set (AGP, compose-bom, verification metadata, FOD hash) are proven before merge.
- `pr-argo-diff.yaml` — runs `argocd app diff` per changed app on PRs touching `k8s-apps/**` and posts the diff as a PR comment.
- `workbench-ci.yaml` — on PRs touching `workbench/**`: runs `go vet`/`go test`/golangci-lint per changed Go app, pytest per changed Python app, and a no-push docker build mirroring the Kaniko context. A changed Go app with an `integration_test.go` also runs it gated with `<APP>_INTEGRATION=1` (helper-process e2e; needs the DERP network, available on hosted runners).
- `workbench-tag-bump.yaml` — on push to main touching `workbench/**`: bumps `build.yaml` (and the matching `k8s-apps` image tag) when a merge modified a workbench app without bumping its tag. Idempotent (any `+tag:` line in the merge window skips the bump), runs serialized, works with Renovate merges too.
- Image builds still happen in-cluster via Kaniko on merge to main; CI builds are validation only.
- Branch protection on main requires the `Workbench CI result` and `argocd_diff` checks before merging (both workflows run on every PR; the workbench one reports a stable aggregate because matrix job names vary per app). With `platformAutomerge`, Renovate's auto-merge only merges once these pass.
