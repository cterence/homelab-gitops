#!/usr/bin/env bash
# Recomputes the vendorHash of Go apps whose go.mod/go.sum changed in a merge
# window. A hash mismatch makes nix print the correct "got:" hash, which
# replaces the stale one; the package is rebuilt to verify. Commits per app as
# "<app>: update vendorHash"; push is left to CI.
#
# Usage: workbench-vendorhash-bump.sh <base-ref> [--dry-run]
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
    continue
  fi

  new=$(sed -n 's/^.*got: *\(sha256-[^ ]*\).*$/\1/p' "$build_log" | head -1)
  if [ -z "$new" ] || [ "$new" = "$old" ]; then
    echo "$app: build failed without a vendorHash mismatch:" >&2
    cat "$build_log" >&2
    exit 1
  fi

  sed -i "s|$old|$new|g" flake.nix
  nix build ".#$app" --no-link

  if [ "$DRY_RUN" != "--dry-run" ]; then
    git add flake.nix
    git commit -q -m "$app: update vendorHash"
  fi
done
