# device-plugin

Exposes host character devices (GPU render nodes, USB serial adapters) as
extended resources so pods can request them without `privileged`.

## How it works

For each configured device the binary registers a gRPC device plugin with the
kubelet (`/var/lib/kubelet/device-plugins`). A pod that requests the resource
gets the device node mounted into the container and allowlisted in its devices
cgroup — the piece plain hostPath mounts are missing on containerd.

Kubelet deletes the plugin sockets when it restarts, and a plugin that does not
register again leaves its resource at capacity `0` on that node: new pods stay
`Insufficient <resource>`, and pods already using the device are killed with
`UnexpectedAdmissionError`. The binary therefore watches its own socket and
re-registers when it disappears, which also recovers the device after a kubelet
restart or upgrade.

## Commands

- `device-plugin` (default) — serve the configured devices until `SIGINT` or
  `SIGTERM`.
- `device-plugin probe` — exit non-zero unless every configured device answers
  on its socket. Used as the DaemonSet liveness probe, so a plugin that stays
  unregistered is restarted.

## Configuration

`--config` (default `config.yaml`) reads both commands:

```yaml
devices:
  - resource: homelab.io/render
    path: /dev/dri/renderD128
    count: 16
  - resource: homelab.io/conbee
    path: /dev/ttyACM0
```

- `resource`: extended resource name, must be `<domain>/<name>`.
- `path`: absolute host path of the device node. Devices missing on a node are
  advertised as `Unhealthy`; plugins register only what the config lists.
- `count`: number of slots advertised (default `1`, exclusive). Only use `>1`
  for shareable device nodes such as GPU render devices; each slot is a
  separate allocation to a pod, all mapped to the same node.

Flags: `--debug` for debug logging.

## Consuming a device

```yaml
resources:
  requests:
    homelab.io/render: 1
```

## Deployment

`k8s-apps/device-plugin` runs the image as a DaemonSet with
`/var/lib/kubelet/device-plugins` and `/dev` (read-only) mounted. Config lives
in a ConfigMap; editing it rolls the DaemonSet via Reloader annotations.
