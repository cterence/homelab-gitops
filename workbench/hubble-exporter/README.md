# hubble-exporter

Collects a deduplicated catalog of network flows from the hubble relay and
logs only novel tuples — the source dataset for authoring egress netpols.
Replaces storing the full flow firehose in loki (~1M events/day) with a few
log lines per day, without losing pair completeness: every distinct
(source, destination, protocol, port, verdict) tuple emits exactly one log
line the first time it is observed.

## Flags

| flag | default | description |
|---|---|---|
| `--server` | `hubble-relay.kube-system.svc.cluster.local:80` | hubble relay address |
| `--pod-cidr` | `10.244.0.0/16` | pod CIDR, used as the relay-side source filter and to classify destinations |
| `--node-cidr` | `192.168.1.0/24` | node LAN CIDR, used to classify destinations |
| `--state` | `/data/seen.json` | persisted seen-keys state |

## Output

Two structured log events on stdout (picked up by the otel loki pipeline):

- `pair`: novel source->destination tuple — `src_ns`, `src_pod`, `dst_kind`
  (`pod`/`node`/`world`/`host`/`remote-node`), `dst_ns`, `dst_pod`, `dst_ip`,
  `proto`, `dst_port`, `verdict`
- `drop`: novel dropped-flow tuple — `src_ns`, `src_pod`, `src_kind`
  (`pod`/`node`/`world`/`host`/`remote-node`, set when the source carries no
  pod identity, e.g. kube-proxy SNAT to the node IP), `dst_ns`, `dst_pod`,
  `dst_kind`, `dst_ip`, `proto`, `dst_port`, `reason` (hubble drop reason)
- `dns`: novel (source namespace, query name) with the answer `ips` — the
  FQDN inventory for toFQDN egress policies

The stream is filtered relay-side to pod- and node-sourced flows and
reconnected with a backoff on errors; state is saved every 5 minutes and on
shutdown, so a restart re-emits nothing that was already recorded.

## Caveats

- The `--pod-cidr` flag must match the cluster's actual pod CIDR.
- DNS records only appear once cilium's DNS proxy selects the source pods
  (observation CNP applied to the measured namespaces).
