#!/usr/bin/env bash
# Bumps workbench/<app>/build.yaml image tags (and the matching tag in
# k8s-apps/<app>/values.yaml) when a PR modifies an app without bumping its
# tag. Runs on every push to a PR; idempotent because the check is a diff
# against the merge base with the base branch: once a PR contains a tag
# change, later runs and commits never bump again.
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

  values="k8s-apps/$app/values.yaml"
  if [ -f "$values" ]; then
    awk -v repo="registry.terence.cloud/$app" -v old="$old" -v new="$new" '
      $0 ~ "repository: " repo { inblock=1; print; next }
      inblock && $0 ~ ("^[[:space:]]*tag: " old "$") { sub(old, new); inblock=0 }
      { print }
    ' "$values" > "$values.tmp"
    mv "$values.tmp" "$values"
  fi

  echo "bumped $app: $old -> $new"
  if [ "$DRY_RUN" != "--dry-run" ]; then
    add_files=("$build_yaml")
    if [ -f "$values" ]; then add_files+=("$values"); fi
    git add "${add_files[@]}"
    git commit -q -m "$app: bump image tag to $new"
  fi
done
