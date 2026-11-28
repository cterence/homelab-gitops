# Workbench

Develop programs and self-host them with declarative in-cluster image builds.

Add a subdir here with a `Dockerfile`, your source, and a `build.yaml`. On
commit, ArgoCD renders a Kaniko Job that builds it and pushes to the in-cluster
registry at `registry.registry:5000`.

## Apps

The AI column is the share of the app's code that was AI-generated, as reported
by the maintainer. It is not measured from the repository.

| App | What it does | AI |
| --- | --- | --- |
| [`agent-relay`](agent-relay/) | Generic webhook to Mistral-agent relay: each route renders a prompt template and starts a conversation. | 100% |
| [`apachewebdav`](apachewebdav/) | Apache httpd image with WebDAV enabled, configured entirely through environment variables. | 0% |
| [`catbox`](catbox/) | Async file transfer between your machines over tailcat, with one always-on stash in the cluster. | 100% |
| [`cnpg-restore-test`](cnpg-restore-test/) | Daily restore verification of every CNPG cluster from its barman-cloud backups, with integrity checks. | 100% |
| [`device-plugin`](device-plugin/) | Exposes host character devices (GPU render nodes, USB serial) as extended resources, no `privileged`. | 100% |
| [`docs`](docs/) | Static documentation site at docs.terence.cloud, served from a Garage bucket. | 25% |
| [`gitea-mirror-sync`](gitea-mirror-sync/) | Reconciles a declarative list of git mirrors against Gitea: creates the missing ones, updates settings, prunes the rest. | 100% |
| [`go-healthcheck`](go-healthcheck/) | Aggregates HTTP and PostgreSQL health checks into a single JSON status endpoint. | 0% |
| [`grafana-dashboards`](grafana-dashboards/) | Builds Grafana dashboards with the Foundation SDK and writes them into the kube-prometheus-stack app. | 100% |
| [`hubble-exporter`](hubble-exporter/) | Logs only novel network flow tuples from the hubble relay, the source dataset for egress netpols. | 100% |
| [`jellystat2jellydash`](jellystat2jellydash/) | Converts a Jellystat backup into the History CSV that Jellydash imports. | 100% |
| [`lastfm-scrobble-deduplicator`](lastfm-scrobble-deduplicator/) | Detects and deletes duplicate Last.fm scrobbles by walking the profile with chromedp. | 10% |
| [`pg2sqlite`](pg2sqlite/) | Converts a plain-format `pg_dump` artifact into a SQLite database, streaming the rows. | 100% |
| [`pv-migrate`](pv-migrate/) | Moves data from a Released PersistentVolume to a destination PVC by wrapping upstream pv-migrate. | 100% |
| [`rangemusique`](rangemusique/) | Arranges music files into `artist/album (year)/artist - album - NN track.ext` from their tags. | 25% |
| [`slackmoji`](slackmoji/) | Turns a GIF, PNG, JPEG or WebP into a Slack emoji under 128 KB and 128x128 px. | 100% |
| [`telegram-hub`](telegram-hub/) | The homelab's single Telegram app: an MCP server plus text to speech in one Go binary. | 100% |

## build.yaml

```yaml
image: my-app      # required: repo path in the registry
tag: v1            # required: bump this to trigger a rebuild
dockerfile: Dockerfile   # optional (default: Dockerfile)
buildArgs:         # optional
  VERSION: "1.2.3"
```

## Rebuilding

The Kaniko Job name embeds the tag (`build-<image>-<tag>`). To rebuild after
changing files, bump `tag` in `build.yaml` and commit. Unchanged tags do not
rebuild.

## Consuming an image

The registry is exposed externally at `registry.snow-delta.ts.net` (TLS via
cert-manager/Letsencrypt, routed through traefik). Reference any built image
from any app or node:

```
registry.snow-delta.ts.net/<image>:<tag>
```

Nodes resolve `registry.snow-delta.ts.net` via public DNS and trust the
Letsencrypt certificate, so no insecure-registry containerd config is needed.
Kaniko pushes in-cluster via `registry.registry:5000` (the service DNS
resolves from within the cluster); pulls go through the external TLS ingress.

## Prerequisite: repository must be public

Builds rely on Kaniko performing an anonymous `git` clone of this repository
(`git://github.com/cterence/homelab-gitops.git`), so the repository must remain
public. If it is made private, a git credential secret must be mounted into the
Kaniko Job.

## Prerequisite: insecure registry for pushes

The registry service is plain HTTP. Kaniko pushes with `--insecure` to the
in-cluster service `registry.registry:5000`. Pulls go through the external
TLS ingress (`registry.snow-delta.ts.net`), so nodes do not need any insecure
registry configuration.

## Managing tags

List and delete tags with `skopeo` (no Docker daemon required):

```bash
# List all tags for a repo
skopeo list-tags docker://registry.snow-delta.ts.net/<image>

# Delete a tag
skopeo delete docker://registry.snow-delta.ts.net/<image>:<tag>
```

After deleting tags, run garbage collection to reclaim orphaned blobs. The GC
CronJob runs daily at 4 AM with `--delete-untagged`, which removes untagged
manifests and their blobs (see `k8s-apps/registry/values.yaml`).

## Deleting a repository

The registry API does not support deleting a repository — only individual
manifests and blobs. After removing all tags and running GC, the empty
repository namespace remains. Delete it directly from the registry's PVC:

```bash
kubectl exec -n registry deploy/registry-main -- \
  rm -rf /var/lib/registry/docker/registry/v2/repositories/<image>
```
