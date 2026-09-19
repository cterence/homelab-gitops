#!/usr/bin/env bash
# Bumps workbench/<app>/build.yaml image tags — and the matching image tag in
# every k8s-apps values file that references the app's image — when a PR
# modifies an app without bumping its tag. References are found by scanning
# k8s-apps/*/values.yaml for the app's repository line, either as
# "repository: registry.terence.cloud/<app>" (app-template) or as a bare
# "repository: <app>" with a separate registry key (e.g. gitea mirrorSync),
# so images consumed as sidecars by other apps are updated too.
# Idempotent per PR: the check is a diff against the merge base, so once a
# PR contains a tag change, later runs and commits never bump again.
#
# Usage: workbench-tag-bump.sh <base-ref> [--dry-run]
# Commits per app as "<app>: bump image tag to <new>"; push is left to CI.

set -euo pipefail

BASE_REF="${1:?usage: workbench-tag-bump.sh <base-ref> [--dry-run]}"
DRY_RUN="${2:-}"

MERGE_BASE=$(git merge-base "$BASE_REF" HEAD)

APPS=$(git diff --name-only "$MERGE_BASE" HEAD -- workbench/ | cut -d/ -f2 | sort -u)

for app in $APPS; do
  build_yaml="workbench/$app/build.yaml"
  [ -f "$build_yaml" ] || continue

  # Already bumped in this PR: any added "tag:" line counts.
  if git diff "$MERGE_BASE" HEAD -- "$build_yaml" | grep -q '^+tag:'; then
    continue
  fi

  old=$(awk '/^tag:/{print $2}' "$build_yaml")
  new="v$(( ${old#v} + 1 ))"

  awk -v new="tag: $new" '/^tag:/{print new; next} {print}' "$build_yaml" > "$build_yaml.tmp"
  mv "$build_yaml.tmp" "$build_yaml"

  echo "bumped $app: $old -> $new"

  add_files=("$build_yaml")
  while IFS= read -r values; do
    awk -v app="$app" -v old="$old" -v new="$new" '
      /^[[:space:]]*repository:/ {
        inblock = ($0 ~ ("^[[:space:]]*repository: (registry\\.terence\\.cloud/)?" app "[[:space:]]*$"))
        print
        next
      }
      inblock && $0 ~ ("^[[:space:]]*tag: " old "[[:space:]]*$") { sub(old, new); inblock=0 }
      { print }
    ' "$values" > "$values.tmp"
    if cmp -s "$values" "$values.tmp"; then
      rm "$values.tmp"
      echo "  WARNING: $values references $app but its tag is not \"$old\" — update manually" >&2
    else
      mv "$values.tmp" "$values"
      add_files+=("$values")
      echo "  tag updated in $values"
    fi
  done < <(grep -lE "^[[:space:]]*repository: (registry\.terence\.cloud/)?$app[[:space:]]*$" k8s-apps/*/values.yaml 2>/dev/null || true)

  if [ "$DRY_RUN" != "--dry-run" ]; then
    git add "${add_files[@]}"
    git commit -q -m "$app: bump image tag to $new"
  fi
done
