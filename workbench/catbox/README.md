# catbox

Async file transfer between your machines over
[tailcat](https://github.com/tailscale/tailcat) (Tailscale's data plane
without the control plane). One always-on **storer** in the cluster,
daemonless peers, everything end-to-end encrypted.

## Model

- **storer** (`catbox serve`): one tailcat listener in the cluster.
  Holds sealed files for offline targets (spool, 30-day TTL, 100G cap),
  keeps the member roster, and hands out pending files on pull.
  Sealed files are opaque to it.
- **peers**: one-shot commands, no resident process. `catbox recv`
  pulls parked files; `catbox recv --stay` additionally receives
  directly while online; senders try the listener first and always
  fall back to the storer.
- Files are sealed to the recipient's node key: 64 KiB
  XChaCha20-Poly1305 chunks under a per-file key wrapped in a sealed
  box (age's STREAM construction). The storer relays ciphertext only.
- Joining is codeless: possessing the storer's tailcat address *is*
  the membership capability (it embeds the WireGuard pre-shared key).

## Commands

```
catbox serve  [--data DIR] [--region N] [--max 100G] [--ttl 720h] [--health :8081]
catbox addr
catbox join   <storer-addr> --name NAME
catbox invite
catbox send   <member> <file>
catbox recv   [--dir DIR] [--stay]
```

- `serve` runs the storer. First boot creates the identity (node key,
  pre-shared key, DERP region baked in) in `--data`; the tailcat
  address is stable for the life of that directory. Logs never contain
  the address or any key material.
- `addr` prints the storer's tailcat address from its data dir:
  `kubectl -n catbox exec catbox-0 -- catbox addr`. Run inside the
  cluster; the address is a bearer capability, treat it like a
  password. It never creates the identity — only `serve` does.
- `join` registers this machine under a lowercase-slug name. Run once
  per machine; the identity lives in the OS config dir
  (`~/.config/catbox` or `~/Library/Application Support/catbox`).
- `invite` prints the join line (with the cached storer address) for
  enrolling another machine.
- `send` seals and ships the file: direct to the target's listener
  when it is online (15s dial timeout), otherwise deposited at the
  storer until the target pulls.
- `recv` is the single receive verb: it pulls everything the storer
  holds for this machine into the inbox dir (OS downloads + `/catbox`
  by default), verifying each file's SHA-256 and acknowledging so the
  storer deletes its copy. With `--stay` it keeps running afterwards
  as a direct-send listener, publishing its tailcat address in the
  roster and clearing it again on shutdown (Ctrl-C).

## Deployment

Deployed as the `catbox` umbrella chart (`k8s-apps/catbox`):
statefulset, one PVC at `/data` (identity, roster, spool — the only
irreplaceable file is `identity.json`), health on `:8081` at
`/healthz`. Image built in-cluster by Kaniko as
`registry.terence.cloud/catbox:<tag>`; bump `build.yaml` and the
chart's image tag together, never reusing a tag.

## v1 limits

No revocation or member removal, no renames, no roster deletions, one
storer, no Android/web client. Losing the storer's data dir means a
new identity and re-joining every peer.
