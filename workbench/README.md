# Workbench

Develop programs and self-host them with declarative in-cluster image builds.

Add a subdir here with a `Dockerfile`, your source, and a `build.yaml`. On
commit, ArgoCD renders a Kaniko Job that builds it and pushes to the in-cluster
registry at `registry.registry:5000`.

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

The registry is exposed externally at `registry.terence.cloud` (TLS via
cert-manager/Letsencrypt, routed through traefik). Reference any built image
from any app or node:

```
registry.terence.cloud/<image>:<tag>
```

Nodes resolve `registry.terence.cloud` via public DNS and trust the
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
TLS ingress (`registry.terence.cloud`), so nodes do not need any insecure
registry configuration.

## Managing tags

List and delete tags with `skopeo` (no Docker daemon required):

```bash
# List all tags for a repo
skopeo list-tags docker://registry.terence.cloud/<image>

# Delete a tag
skopeo delete docker://registry.terence.cloud/<image>:<tag>
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
