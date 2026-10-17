#!/usr/bin/env bash
# Recomputes the vendorHash of every Go app whose go.mod/go.sum changed in a
# merge window. Renovate's gomod updates rewrite workbench/<app>/go.mod without
# knowing that flake.nix content-addresses the go-modules fixed-output
# derivation by vendorHash: a stale hash makes the cached vendor tree fail
# go's vendoring consistency check against the new go.mod, and the breakage
# only surfaces downstream in nixos-config builds.
#
# For each changed app the script finds the buildGo*Module block in flake.nix
# whose src is ./workbench/<app>, extracts its vendorHash, and rebuilds the
# package: a hash mismatch prints the correct "got:" hash, which replaces the
# stale one (both the package and the android variant share the value). The
# rebuild also serves as verification: the final nix build must succeed.
#
# Idempotent per merge window: the check is a diff against the pre-merge tip,
# and if the hash already matches, nix build succeeds and nothing is committed.
#
# Usage: workbench-vendorhash-bump.sh <base-ref> [--dry-run]
# Commits per app as "<app>: update vendorHash"; push is left to CI.
set -euo pipefail

BASE_REF="${1:?usage: workbench-vendorhash-bump.sh <base-ref> [--dry-run]}"
DRY_RUN="${2:-}"

MERGE_BASE=$(git merge-base "$BASE_REF" HEAD)
APPS=$(git diff --name-only "$MERGE_BASE" HEAD -- workbench/ | cut -d/ -f2 | sort -u)

for app in $APPS; do
  [ -f "workbench/$app/go.mod" ] || continue

  old=$(awk -v app="./workbench/$app" '
    /buildGo[0-9]+Module/ { inblock = 1 }
    inblock && /src = / { matched = ($0 ~ app) }
    inblock && /vendorHash = "/ {
      line = $0
      start = index(line, "\"") + 1
      end = index(substr(line, start), "\"") - 1
      if (matched) print substr(line, start, end)
      inblock = 0; matched = 0
    }
  ' flake.nix)

  [ -n "$old" ] || continue

  build_log=$(mktemp)
  if nix build ".#$app" --no-link 2>&1 | tee "$build_log"; then
    echo "$app: vendorHash unchanged"
    continue
  fi

  new=$(sed -n 's/^.*got: *\(sha256-[^ ]*\).*$/\1/p' "$build_log" | head -1)
  if [ -z "$new" ] || [ "$new" = "$old" ]; then
    echo "$app: build failed without a vendorHash mismatch — update manually:" >&2
    cat "$build_log" >&2
    exit 1
  fi

  sed -i "s|$old|$new|g" flake.nix
  nix build ".#$app" --no-link

  if [ "$DRY_RUN" != "--dry-run" ]; then
    git add flake.nix
    git commit -q -m "$app: update vendorHash"
  fi
  echo "$app: vendorHash $old -> $new"
done
