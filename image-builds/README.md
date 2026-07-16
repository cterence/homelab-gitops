# Declarative image builds

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

Reference it in any app: `registry.registry:5000/<image>:<tag>`.

## Prerequisite: repository must be public

Builds rely on Kaniko performing an anonymous `git` clone of this repository
(`git://github.com/cterence/homelab-gitops.git`), so the repository must remain
public. If it is made private, a git credential secret must be mounted into the
Kaniko Job.

## Prerequisite: insecure registry for pulls

The registry is plain HTTP. Kaniko pushes with `--insecure`. For nodes to
**pull** these images, each node's container runtime must treat
`registry.registry:5000` as an insecure registry. Configure containerd's
registry hosts (e.g. `/etc/containerd/certs.d/registry.registry:5000/hosts.toml`
with `skip_verify = true` / `http`) or the equivalent for your runtime. If you
prefer not to touch node config, switch the registry to TLS instead (out of
scope for this iteration).
