# gitea-mirror-sync

Reconciles a declarative list of git mirrors against a running Gitea
instance. Creates missing mirrors via the migrate API, updates settings
(mirror interval, private flag) on existing ones, and prunes mirrors that
are no longer in the config.

## How it works

1. **Load config** — Reads a YAML file (default `/etc/mirrors/mirrors.yaml`)
   declaring the desired mirrors. Derives repo names from clone URLs when
   not specified, applies a default owner and mirror interval, and
   validates for duplicates.

2. **Reconcile desired state** — For each declared mirror, checks whether
   the repo already exists:
   - **Missing** — creates it via `/api/v1/repos/migrate` with `mirror: true`.
   - **Existing mirror** — patches `mirror_interval` and `private` if they
     differ from the desired state.
   - **Existing non-mirror** — refuses to touch it (refuses to convert a
     regular repo into a mirror).

3. **Prune** — Lists all mirror repos owned by the managed owners and
   deletes any that are not in the desired config. Non-mirror repos are
   never touched. Disabled with `PRUNE=false`.

## Usage

```bash
# Dry run (default): log what would change, don't call mutating APIs
GITEA_URL=https://gitea.example.com \
GITEA_USER=user \
GITEA_PASS=token \
go run .

# Full run
DRY_RUN=false GITEA_URL=https://gitea.example.com GITEA_USER=user GITEA_PASS=token go run .

# Custom config path, prune disabled
MIRRORS_FILE=./mirrors.yaml PRUNE=false DRY_RUN=false go run .
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GITEA_URL` | (empty) | Gitea base URL (e.g. `https://gitea.example.com`). |
| `GITEA_USER` | (empty) | Gitea username for basic auth. |
| `GITEA_PASS` | (empty) | Gitea token / password for basic auth. |
| `MIRRORS_FILE` | `/etc/mirrors/mirrors.yaml` | Path to the mirrors config file. |
| `DRY_RUN` | `true` | Log planned changes without applying them. Set `false` to apply. |
| `PRUNE` | `true` | Delete mirrors not present in the config. |

## Configuration

The mirrors file is a YAML document with a default owner, a default mirror
interval, and a list of mirrors. Only `clone_addr` is required per entry;
`name` is derived from the URL and `owner` falls back to `defaultOwner`.

```yaml
defaultOwner: mirrors
defaultMirrorInterval: 8h
mirrors:
  - clone_addr: https://github.com/cterence/homelab-gitops.git
    private: false
    wiki: true
  - owner: personal
    clone_addr: https://github.com/golang/go.git
    name: go                    # override derived name
    mirror_interval: 1h
```

| Field | Default | Description |
|-------|---------|-------------|
| `clone_addr` | (required) | Upstream URL to mirror. |
| `owner` | `defaultOwner` | Gitea owner (user or org) for the mirror. |
| `name` | derived from URL | Repository name. |
| `mirror_interval` | `defaultMirrorInterval` | Gitea mirror pull interval (e.g. `8h`). |
| `private` | `false` | Whether the mirror is private. |
| `wiki` | `false` | Whether to mirror the wiki. |

## Deployment

The image is built via Kaniko from `workbench/gitea-mirror-sync/`.

Deployment is handled by the **Gitea Helm chart** at `k8s-apps/gitea/`, under
the `mirrorSync` values key (`templates/mirror-sync.yaml`). It renders:

- A **ConfigMap** holding `mirrors.yaml`, generated from
  `mirrorSync.mirrors` / `defaultOwner` / `defaultMirrorInterval`.
- A **CronJob** (schedule `0 */6 * * *`, `concurrencyPolicy: Forbid`) that
  mounts the ConfigMap at `/etc/mirrors`, reads Gitea credentials from the
  `gitea-admin-credentials` Secret, and runs the reconciler.
- `PRUNE` is wired from `mirrorSync.prune` (default `true`). `DRY_RUN` is not
  set by the chart, so it defaults to `true` — set it explicitly to `false`
  in the env block to apply changes.

Enable/disable the whole thing with `mirrorSync.enabled` (default `true`).

## Safety

- Non-mirror repos are never modified or deleted — the reconciler only
  manages repos it created as mirrors.
- `DRY_RUN=true` (default) makes no API calls beyond reads, so a misaligned
  config can be previewed safely.
- The run has a 5-minute timeout; reconcile and prune are idempotent, so a
  retry simply resumes from the current state.
