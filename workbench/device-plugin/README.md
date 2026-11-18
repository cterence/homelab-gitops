# device-plugin

Exposes host character devices (GPU render nodes, USB serial adapters) as
extended resources so pods can request them without `privileged`.

## How it works

For each configured device the binary registers a gRPC device plugin with the
kubelet (`/var/lib/kubelet/device-plugins`). A pod that requests the resource
gets the device node mounted into the container and allowlisted in its devices
cgroup — the piece plain hostPath mounts are missing on containerd.

## Configuration

`--config` (default `config.yaml`) reads:

```yaml
devices:
  - resource: homelab.io/render
    path: /dev/dri/renderD128
  - resource: homelab.io/conbee
    path: /dev/ttyACM0
```

- `resource`: extended resource name, must be `<domain>/<name>`.
- `path`: absolute host path of the device node. Devices missing on a node are
  advertised as `Unhealthy`; plugins register only what the config lists.

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
