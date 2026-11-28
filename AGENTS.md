# Agents instructions

## Modifications

For any modifications in this repo, ask the user whether to edit on the main branch without commiting or use git worktrees.
If using git worktrees:

- use the vibe:worktree skill; worktrees live under $VIBE_HOME/worktrees.
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
docs/runbooks/   Runbooks for recurring manual interventions
terraform/     Infrastructure provisioning (secrets, external-secrets)
```

## Secrets

Secrets are provisioned by `terraform/secrets/` from the SOPS-encrypted
`secrets.yaml`. Keys follow `<namespace>.<secret-name>.<key>`; each
`<namespace>/<secret-name>` becomes one k8s Secret (write-only, values never
land in terraform state). Rules:

- Agents must never run sops operations themselves (decrypt, encrypt, rotate,
  edit). Write the terraform/chart wiring only; the user adds sops entries and
  applies with their key.
- To reuse an existing secret in another namespace, the user copies the values
  into new sops keys under the target namespace
  (`<namespace>.<secret-name>.<key>`) — the generic provisioner handles the
  rest. No per-secret terraform resources.
- Prefer creating the secret in the target namespace with terraform over
  external-secrets cross-namespace sharing. ES (ClusterSecretStore per source
  namespace + RBAC in `k8s-apps/external-secrets/values.yaml` +
  ExternalSecret in the consumer) is only warranted for wide distribution,
  e.g. `registry-pull-secret` fanned out to many namespaces.

## Deployed apps

An app is deployed if and only if `k8s-apps/<name>/appset.yaml` exists (the
ApplicationSet git generator matches `k8s-apps/*/appset.yaml`). That file also
carries per-app flags: `namespace`, `serverSideApply`,
`scaleToZero`. Undeployed charts live in `k8s-apps/archive/`.

New apps pulling from `registry.snow-delta.ts.net` need two things, both easy to
forget (missing either leaves the app in ImagePullBackOff): their namespace in
the `registry-pull-secret` ClusterExternalSecret's `spec.namespaces` in
`k8s-apps/registry/values.yaml` — the registry app distributes its own pull
secret — and `imagePullSecrets: [{name: registry-pull-secret}]` under
`app-template.defaultPodOptions` in the app's own values.yaml.

A new app also needs a namespace entry in `k8s-apps/network-policies/values.yaml`.
That chart renders policies only for the namespaces it lists, so an unlisted app
gets no policy at all and accepts ingress from every pod in the cluster. Give it
`defaultDeny: true` plus an `allow:` list of the callers that legitimately reach
it: `traefik` for a `terence.cloud` ingress, `tailscale-operator` for a tailscale
Ingress or an exposed Service, `monitoring` when Prometheus scrapes it,
`go-healthcheck` when that app probes it. Kubelet probes need nothing — the chart
always allows `nodeCIDR`.

Tool/API documentation for workbench apps lives in each app's `README.md` — update it whenever an app's interface (tool arguments, endpoints, env vars) changes.

### App data (SQLite and backups)

An app that stores SQLite — one file or several — forces two separate decisions.
Ask the user which way each goes; do not assume, and do not add backups by
reflex. Durability is not the same as value: shortlinks, caches and rebuildable
indexes do not need either mechanism.

1. Dump it? Worth a dump when losing the file loses data that cannot be
   recreated. If yes, set `dump.enabled: true` in the app's values.yaml (the
   ApplicationSet already renders `charts/sqlite-dump` for every app, disabled
   by default), list each database in `dump.databases` as a path *relative to
   the PVC root* — the dump pod mounts that PVC at `/src`, so a db the app sees
   at `/home/nonroot/golink.db` becomes `/src/golink.db` — and point
   `dump.persistence.config.existingClaim` at the claim name. Dumps land in
   `dumps/` on the same volume, pruned after 3 days: they survive a database
   swap, not the loss of the PVC.
2. Velero it? Worth it only for state that must outlive the volume, snapshotted
   off-cluster. Add the namespace to *both* `includedNamespaces` lists under
   `schedules` in `k8s-apps/velero/values.yaml` (`backblaze` daily,
   `garage-backup` every 8h); PVCs are snapshotted unless `volume-policy` skips
   them. The two lists are duplicated by hand and drift silently.

Record the outcome in the app's `README.md` when the answer is "no" to both, so
the next agent does not re-litigate it. Precedent: `golink` keeps only
shortlinks and click counts, so it gets neither.

## Grafana dashboards

Dashboards are code: builders live in `workbench/grafana-dashboards/` (one
file per dashboard, registered in `main.go`). Add a builder, run `go run .`
from that directory, and commit the regenerated JSON in
`k8s-apps/kube-prometheus-stack/dashboards/` together with the code. That
app's README has the full workflow.

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
   build in-cluster, pushing `registry.snow-delta.ts.net/<app>:<tag>` to the
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
- Always use github.com/urfave/cli/v3 and cli-style skill
- `main()` calls a `run() error` function; on error print to stderr and exit non-zero.
- Exit codes are part of the interface: `0` success, `1` runtime failure, `2` bad
  usage, `4` input not found, `5` conflict/precondition, `130`/`143` interrupted
  by `SIGINT`/`SIGTERM`. Under `--json`, report the failure as a record on
  stderr with the code, message and fix.
- Imports grouped in three blocks separated by blank lines: stdlib, third-party, local (local not needed yet since everything is `package main`).
- Error handling: wrap with context using `fmt.Errorf("doing X: %w", err)`. Never both log and return an error — pick one.
- Logging: `log/slog` with structured key-value pairs, always. A long-running
  service, Job or sidecar logs JSON to stdout. A CLI a human runs interactively
  logs readable text to stderr and switches to JSON with `--json`, so stdout
  stays free for data (`workbench/pg2sqlite` is the reference). Older CLIs that
  log JSON to stdout keep doing so until their next change.
- `context.Context` as first parameter on all methods that do I/O or are long-running.
- Concurrency: prefer `errgroup.WithContext` with `g.SetLimit()` for bounded parallelism.
- Graceful shutdown: use `signal.NotifyContext` for signal handling, or catch the signal directly when the exit code must tell `SIGINT` from `SIGTERM`.

### Tests

- Test files live alongside source as `<name>_test.go` in the same `package main`.
- Table-driven tests using anonymous structs with `t.Run(tt.name, ...)`.
- The suite runs offline with `go test ./...`: no cluster, no network, no
  external services. In-memory databases and temporary directories are fine.
- Fuzz targets live in `fuzz_test.go` (`FuzzXxx`), seed synthetic fixtures only —
  never real customer data — and inputs that once failed are kept in
  `testdata/fuzz/` so they replay on every run.
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
- Workbench app images reference the in-cluster registry: `registry.snow-delta.ts.net/<app>` with a `tag` matching `build.yaml`.
- Third-party images are pinned by digest (`tag@sha256:...`).

## Falco (k8s-apps/falco/)

- In Falco, a macro redefined in a later rules file overrides the earlier definition (unlike lists, which append). Never define the same macro name in more than one `customRules` entry — merge all branches into a single `condition` with `or`. A duplicate silently kills the earlier whitelist (this caused the kaniko "Read sensitive file untrusted" alert spam; see PR #1272).
- Validate changes with `scripts/validate-falco-rules.sh` (needs docker + yq).

## Comments

Do not write comments unless there is a sensible justification for one.

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
