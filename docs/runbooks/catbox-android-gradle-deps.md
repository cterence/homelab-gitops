# Runbook: Renovate PRs bumping catbox Android dependencies

## Background

The catbox APK builds hermetically (see `workbench/catbox/nix/android.nix`):

- `gradle/verification-metadata.xml` pins a sha256 for every maven
  artifact (`gradle` dependency verification).
- The `catbox-gradle-deps` fixed-output derivation (FOD) snapshots the
  full maven repo; the APK derivation replays it with `--offline`.

A Renovate PR that bumps an AndroidX/compose version changes the
resolved artifact set, so three things go stale at once: the
verification metadata, the FOD hash, and sometimes the compileSdk
itself. Renovate updates none of them, so `catbox-pr` CI fails; its
`Repair stale nix hashes` step only fixes bare hashes and reports
"build failed for a reason other than a stale hash".

## Symptoms

- `catbox-gradle-deps` FOD fails with "you will need to update the
  gradle/verification-metadata.xml file".
- APK build fails with "No cached version of X available for offline
  mode".
- `nix-update-hash` repair step fails with "reason other than a stale
  hash".
- After a dep bump, AAR metadata check may fail: "requires libraries
  and applications that depend on it to compile against version 37.1
  or later" — the bump needs a newer compileSdk, not just hashes.

## Fix (on the PR branch)

1. Checkout the PR branch (or a worktree / `gh pr checkout`).
2. If the AAR metadata check demands a newer compileSdk:
   - `workbench/catbox/android/app/build.gradle.kts`: bump
     `compileSdk` and add `compileSdkMinor` (AGP 9 DSL).
   - `workbench/catbox/nix/android.nix` and the `.#android` devshell
     SDK list in `flake.nix`: add the matching
     `platforms-android-<maj>-<min>` package (and bump
     `build-tools`/aapt2 override if the build asks).
3. Regenerate verification metadata:
   `bash workbench/catbox/android/regen-metadata.sh`
4. Rekey the FOD: set `outputHash` in `android.nix` to any fake
   `sha256-AAAA...=`, run `nix build .#catbox-android`, copy the
   `got:` sha256 into `outputHash`. (A stale real hash can hit the
   binary cache, so no mismatch is printed — that is why the fake.)
5. Verify: `nix build .#catbox .#catbox-android-bin .#catbox-android`.
6. Commit and push to the PR branch. Renovate automerges once
   `catbox-pr` is green.

## Gotchas

- Renovate force-rebases its branches when main moves, dropping
  commits pushed to them. Rebase and re-push; the regen/FOD work is
  fast. The FOD hash only changes if the resolved dep set changed.
- The repair step in `nix-cache.yaml` only runs when the push actor is
  renovate or the bump bot, so repair commits pushed by a human do
  not self-heal further stale hashes.
- After any rebase onto a main that itself changed catbox deps,
  redo steps 3-5 — the resolved artifact set changed.
- Prefer merging quickly after pushing fixes, before main moves
  again.
