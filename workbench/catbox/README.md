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
  key, so concurrent client commands queue on the local lock and run
  one at a time, in arrival order.
- Files are sealed to the recipient's node key: 64 KiB
  XChaCha20-Poly1305 chunks under a per-file key wrapped in a sealed
  box (age's STREAM construction). The storer relays ciphertext only.
- Membership is admin-controlled. The first member to join an empty
  mesh bootstraps as admin; every join after that must present a
  one-time invite code an admin minted (`catbox invite`, 24h TTL).
  Admins can also remove other members over the wire; the storer
  address alone is no longer a membership capability.

## Commands

```
catbox serve  [--data DIR] [--region N] [--max 100G] [--ttl 720h] [--health :8081]
catbox addr
catbox join   <name> <invite-token>   # admitted join
catbox join   <name> <storer-addr>     # fresh mesh: the bootstrap admin
catbox rename <new-name>
catbox invite
catbox remove [<member>] | --data DIR <member>
catbox admin  --data DIR <member>
catbox reset --yes
catbox send   <member> <file> [<file>...]
catbox recv   [--dir DIR] [--listen] [<id>...]
catbox dismiss <id>
catbox status
catbox version
```

- `serve` runs the storer. First boot creates the identity (node key,
  pre-shared key, DERP region baked in) in `--data`; the tailcat
  address is stable for the life of that directory. Logs never contain
  the address or any key material.
- `addr` prints the storer's tailcat address from its data dir:
  `kubectl -n catbox exec catbox-0 -- catbox addr`. Run inside the
  cluster; the address is a bearer capability, treat it like a
  password. It never creates the identity — only `serve` does.
- `join <name> <token>` registers this machine under a lowercase-slug
  name. The token is the invite from `catbox invite` — a base64 blob
  carrying the one-time code and naming the storer; a bare storer
  address instead is the fresh-mesh form, no code needed, and the
  first member bootstraps as admin. Run once per machine; the identity
  lives in the OS config dir (`~/.config/catbox` or
  `~/Library/Application Support/catbox`). A join that fails after
  creating the identity rolls it back — a half-joined device is
  never locked out of joining again.
- `invite` (admin) mints a one-time join code (24h TTL, single use,
  survives a storer restart) and prints it as one base64 token, ready
  to hand to the joiner. The Android app's join card takes the token
  as a paste.
- `rename <new-name>` changes this member's name in the roster; parked
  files follow the new name, and a taken or invalid name is rejected.
  Stop `recv --listen` first: renaming re-registers without the
  listener address, which would otherwise go stale.
- `remove <member>` drops another member from the roster, parked
  items and all — admin only, over the wire. `remove --data DIR
  <member>` is the storer-host form: it removes any member without
  being admin, the cleanup path for a device that reset while
  offline and left a ghost. Run it inside the cluster:
  `kubectl -n catbox exec catbox-0 -- catbox remove --data /data <member>`.
  Both rewrite the roster and sweep the spool; the running storer
  picks the edit up on its next message.
- `admin --data DIR <member>` grants admin on the storer host: the
  bootstrap for meshes that predate admins, and the recovery path
  when the last admin reset (an admin-less mesh admits no joins until
  someone runs it).
- `reset --yes` leaves the mesh and wipes this machine's identity: it
  removes its own roster entry (the removal is bound to this device's
  dial key — a device can never reset or remove another member) and
  deletes the local identity and roster cache. When the storer is
  unreachable the reset still completes; clean up the leftover entry
  later with `remove` on the storer.
- `send` seals and ships each named file (one member, one or more
  files per command): direct to the target's listener
  when it is online (3s dial timeout), otherwise deposited at the
  storer until the target pulls. Direct transfers abort after 30
  seconds of silence; a failed direct attempt is retried every few
  seconds for two minutes before the fallback — each redial resumes
  from the listener's partial, so a phone that locks and comes back
  finishes direct with no storer bytes. Storer-path transfers abort
  after two minutes; Ctrl-C aborts instantly.
  Interrupted transfers resume at either end: the receiver keeps its
  received bytes in a content-keyed partial (`.part-<sha12>-<size>` in
  the inbox), and the storer keeps an interrupted deposit's partial
  in its spool — the next attempt, direct, deposit, or pull, continues
  at its 64 KiB chunk boundary instead of starting over. Retried
  deposits are idempotent: when the storer already parks the same
  content for the same target, it answers so and no bytes move. One
  receiver per partial: a pull and a direct receive of the same
  content never interleave — the second declines, and the sender's
  fallback finds the item already parked. The file's
  SHA keys a deterministic per-file secret, so a resumed attempt
  decrypts against the existing partial. Stale partials (1 hour
  untouched) are swept at `recv` startup and hourly by `serve`; a
  SHA mismatch deletes the partial outright, and so does an
  imperative cancel (Ctrl-C, the app's cancel button) — only an
  accidental interruption keeps the partial for the resume. Output
  marks a resume with `resumed from N`. Both receive paths announce
  before the bytes (`<peer> is sending <file> directly (size)` for
  direct, `receiving <file> from <peer> (size)` per pulled file), so
  the CLI log and the app's in-flight row always name the file.
- `recv` without `--listen` pulls held files into the inbox dir (OS
  downloads + `/catbox` by default) — everything, or only the files
  named by id (see `catbox status`) — verifying each file's SHA-256
  and acknowledging so the storer deletes its copy; an unknown id is
  an error. `recv --listen` only listens: it publishes its tailcat
  address in the roster and clears it again on shutdown (Ctrl-C);
  pulling stays an explicit decision.
- `dismiss <id>` refuses delivery of one of your own pending items:
  the storer deletes it without the bytes ever transferring. `status`
  lists the ids of everything waiting for you.
- `status` asks the storer who's in the mesh: files and bytes waiting
  for you there (each with its id, sender, and size), and every member
  with `[admin]`, `[you]`, and `(listening)` markers. The roster comes
  straight from the authority, never from the local cache. `--json`
  emits one machine-readable object (name, waiting items, members)
  for the Android app.
- `version` prints the build stamp baked in via `-ldflags
  "-X main.version=…"`: the git short rev in nix builds, the image tag
  in Docker builds, `dev` otherwise.
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
on screen — direct sends are always welcome, every waiting file
carries its own receive action, and the waiting header receives
everything at once; parked files can be refused per file with a
confirming dialog. The overflow menu's Reset leaves the mesh and wipes
the app's identity (the binary's `reset`, confirmation dialog first);
the join card takes the
invite token as a paste (or a bare storer address on a fresh mesh).
Its server engine (identity key) never conflicts
with one-shot client execs (dial key). While an own-action transfer
is in flight the app holds a bounded partial wake lock, so screen-off
does not suspend it mid-transfer; a listener receive ends with the
screen, its partial resuming wherever the next attempt picks up.

Build (hermetic, offline gradle, pinned debug keystore dedicated to
catbox) — run from this directory:

    make catbox-apk        # → result.apk, ready for adb install

Iterating on the Kotlin in the devshell:

    nix develop ../..#android
    ./android/build-native.sh   # the catbox binary into jniLibs
    cd android && gradle assembleDebug

## v1 limits

Admins mint invites and remove members; there is no demote, transfer,
or second-admin promotion over the wire — `admin --data DIR` on the
storer covers those. No renames beyond `rename`, one storer. Losing
the storer's data dir means a new identity and re-joining every peer
(invites too: they live in the data dir alongside the roster).
