#!/bin/bash
# Compares Kubernetes PersistentVolumes (from local-path-provisioner) with the
# actual directories on disk, grouped by base path (e.g.
# /opt/local-path-provisioner, /mnt/storage-pool/local-path-provisioner).
# Base paths that do not exist on this node are ignored.
# Outputs only discrepancies: orphaned PVs (no directory) and orphaned dirs
# (no PV), prefixed with the hostname since PVs may live on homelab2 or homelab3.
#
# Usage: bash pv-diff.sh [BASE_PATH]
# BASE_PATH (default /opt/local-path-provisioner) is always checked, so
# orphaned directories under it are reported even when no PV references it.

set -euo pipefail

BASE_PATH="${1:-/opt/local-path-provisioner}"
HOST="$(hostname)"

# Full paths of local-path-provisioner PVs bound to this node
PV_PATHS=$(kubectl get pv -o json 2>/dev/null | jq -r --arg h "$HOST" '
  .items[]
  | select(.spec.local.path // "" | contains("/local-path-provisioner/"))
  | select(
      (.spec.nodeAffinity.required.nodeSelectorTerms[].matchExpressions[]
       | select(.key == "kubernetes.io/hostname" and .operator == "In")
       | .values[]
      ) == $h
    )
  | .spec.local.path
')

# Candidate base paths: parents of node-bound PV paths, plus BASE_PATH
ALL_BASES=$(
  {
    printf '%s\n' "$PV_PATHS" | awk 'NF { sub(/\/[^/]+$/, ""); print }'
    printf '%s\n' "$BASE_PATH"
  } | sort -u
)

# Ignore base paths that do not exist on this node
BASES=$(printf '%s\n' "$ALL_BASES" | while IFS= read -r d; do if [ -d "$d" ]; then printf '%s\n' "$d"; fi; done)
IGNORED_BASES=$(printf '%s\n' "$ALL_BASES" | while IFS= read -r d; do if [ ! -d "$d" ]; then printf '%s\n' "$d"; fi; done)

clean() { printf '%s\n' "$1" | awk 'NF'; }
count() { printf '%s\n' "$1" | awk 'NF' | wc -l | tr -d ' '; }

TOTAL_ORPHAN_PVS=0
TOTAL_ORPHAN_DIRS=0

echo "=== [$HOST] PV/directory discrepancies ==="
if [ -n "$(clean "$IGNORED_BASES")" ]; then
    echo "  Ignored base paths (not present on this node):"
    clean "$IGNORED_BASES" | sed 's/^/    /'
fi
echo

while IFS= read -r BASE; do
    PV_NAMES=$(printf '%s\n' "$PV_PATHS" | awk -F/ -v p="$BASE/" 'index($0, p) == 1 { print $NF }' | sort -u)
    DIR_NAMES=$(ls -1 "$BASE" 2>/dev/null | awk 'NF' | sort)

    ORPHAN_PVS=$(comm -23 <(clean "$PV_NAMES") <(clean "$DIR_NAMES"))
    ORPHAN_DIRS=$(comm -13 <(clean "$PV_NAMES") <(clean "$DIR_NAMES"))

    ORPHAN_PV_COUNT=$(count "$ORPHAN_PVS")
    ORPHAN_DIR_COUNT=$(count "$ORPHAN_DIRS")
    TOTAL_ORPHAN_PVS=$((TOTAL_ORPHAN_PVS + ORPHAN_PV_COUNT))
    TOTAL_ORPHAN_DIRS=$((TOTAL_ORPHAN_DIRS + ORPHAN_DIR_COUNT))

    echo "--- $BASE ---"
    echo "  PVs: $(count "$PV_NAMES"), Dirs: $(count "$DIR_NAMES"), Orphan PVs: $ORPHAN_PV_COUNT, Orphan Dirs: $ORPHAN_DIR_COUNT"

    if [ -n "$(clean "$ORPHAN_PVS")" ]; then
        echo "  Orphaned PVs (PV exists, directory missing):"
        clean "$ORPHAN_PVS" | sed 's/^/    /'
    fi
    if [ -n "$(clean "$ORPHAN_DIRS")" ]; then
        echo "  Orphaned directories (directory exists, no PV):"
        clean "$ORPHAN_DIRS" | sed "s|^|    $BASE/|"
    fi
    echo
done < <(printf '%s\n' "$BASES" | awk 'NF')

echo "Total Orphan PVs: $TOTAL_ORPHAN_PVS, Total Orphan Dirs: $TOTAL_ORPHAN_DIRS"
