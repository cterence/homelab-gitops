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
  pulls parked files; `catbox recv --listen` receives direct sends
  while online (it never pulls); senders try the listener first and
  always fall back to the storer. tailcat allows one tunnel per peer
  key, so concurrent client commands serialize: the second fails
  fast ("another catbox command is already running") instead of
  hanging.
- Files are sealed to the recipient's node key: 64 KiB
  XChaCha20-Poly1305 chunks under a per-file key wrapped in a sealed
  box (age's STREAM construction). The storer relays ciphertext only.
- Joining is codeless: possessing the storer's tailcat address *is*
  the membership capability (it embeds the WireGuard pre-shared key).

## Commands

```
catbox serve  [--data DIR] [--region N] [--max 100G] [--ttl 720h] [--health :8081]
catbox addr
catbox join   --name NAME <storer-addr>
catbox rename <new-name>
catbox invite
catbox send   <member> <file>
catbox recv   [--dir DIR] [--listen]
catbox dismiss <id>
catbox status
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
- `rename <new-name>` changes this member's name in the roster; parked
  files follow the new name, and a taken or invalid name is rejected.
  Stop `recv --listen` first: renaming re-registers without the
  listener address, which would otherwise go stale.
- `send` seals and ships the file: direct to the target's listener
  when it is online (3s dial timeout), otherwise deposited at the
  storer until the target pulls. Transfers abort after two minutes
  of silence and fall back to the storer; Ctrl-C aborts instantly.
- `recv` without `--listen` pulls everything the storer holds for
  this machine into the inbox dir (OS downloads + `/catbox` by
  default), verifying each file's SHA-256 and acknowledging so the
  storer deletes its copy. `recv --listen` only listens: it publishes
  its tailcat address in the roster and clears it again on shutdown
  (Ctrl-C); pulling stays an explicit decision.
- `dismiss <id>` refuses delivery of one of your own pending items:
  the storer deletes it without the bytes ever transferring. `status`
  lists the ids of everything waiting for you.
- `status` asks the storer who's in the mesh: files and bytes waiting
  for you there (each with its id, sender, and size), and every member
  with `[you]` and `(listening)` markers. The roster comes straight
  from the authority, never from the local cache. `--json` emits one
  machine-readable object (name, waiting items, members) for the
  Android app.
- Received files never overwrite: a name collision in the inbox
  becomes `name(1).ext`, `name(2).ext`, and so on.

## Deployment

Deployed as the `catbox` umbrella chart (`k8s-apps/catbox`):
statefulset, one PVC at `/data` (identity, roster, spool — the only
irreplaceable file is `identity.json`), health on `:8081` at
`/healthz`. Image built in-cluster by Kaniko as
`registry.terence.cloud/catbox:<tag>`; bump `build.yaml` and the
chart's image tag together, never reusing a tag.

## Android

`android/` is a thin single-screen Compose app around the same
binary: cross-compiled `GOOS=android` as `libcatbox.so`, exec'd as a
child process with `HOME` pointed at the app's private storage (the
identity lives there) and the inbox at the app's external files dir
via `--dir`. The app runs one-shot `status --json` / `send` / `recv` /
`dismiss` and holds a long-lived `recv --listen` child whenever it is
on screen — direct sends are always welcome, while storer pulls wait
behind the explicit receive button, and parked files can be refused
per file with a confirming dialog. Its server engine (identity key)
never conflicts with one-shot client execs (dial key).

Build (hermetic, offline gradle, pinned debug keystore dedicated to
catbox):

    make catbox-apk        # → result.apk, ready for adb install

Iterating on the Kotlin in the devshell:

    nix develop .#android
    ./android/build-native.sh   # the catbox binary into jniLibs
    cd android && gradle assembleDebug

## v1 limits

No revocation or member removal, no renames, no roster deletions, one
storer, no Android/web client. Losing the storer's data dir means a
new identity and re-joining every peer.
