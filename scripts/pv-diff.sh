#!/bin/bash
# Compares Kubernetes PersistentVolumes (from local-path-provisioner) with the
# actual directories on disk under /opt/local-path-provisioner.
# Outputs only discrepancies: orphaned PVs (no directory) and orphaned dirs
# (no PV), prefixed with the hostname since PVs may live on homelab2 or homelab3.
#
# Usage: bash pv-diff.sh [BASE_PATH]
# Default BASE_PATH: /opt/local-path-provisioner

set -euo pipefail

BASE_PATH="${1:-/opt/local-path-provisioner}"
HOST="$(hostname)"

# Get PV directory names from local-path-provisioner, filtered to PVs bound to this node
PV_NAMES=$(kubectl get pv -o json 2>/dev/null | jq -r --arg h "$HOST" '
  .items[]
  | select(.spec.local.path // "" | contains("/local-path-provisioner/"))
  | select(
      (.spec.nodeAffinity.required.nodeSelectorTerms[].matchExpressions[]
       | select(.key == "kubernetes.io/hostname" and .operator == "In")
       | .values[]
      ) == $h
    )
  | .spec.local.path | split("/") | last
' | sort)

# Get directories on disk
DIR_NAMES=$(ls -1 "$BASE_PATH" 2>/dev/null | sort)

# Compare
ORPHAN_PVS=$(comm -23 <(echo "$PV_NAMES") <(echo "$DIR_NAMES"))
ORPHAN_DIRS=$(comm -13 <(echo "$PV_NAMES") <(echo "$DIR_NAMES"))

# Summary counts
PV_COUNT=$(echo "$PV_NAMES" | grep -c . 2>/dev/null || echo 0)
DIR_COUNT=$(echo "$DIR_NAMES" | grep -c . 2>/dev/null || echo 0)
ORPHAN_PV_COUNT=$(echo "$ORPHAN_PVS" | grep -c . 2>/dev/null || echo 0)
ORPHAN_DIR_COUNT=$(echo "$ORPHAN_DIRS" | grep -c . 2>/dev/null || echo 0)

echo "=== [$HOST] PV/directory discrepancies ($BASE_PATH) ==="
echo "  PVs: $PV_COUNT, Dirs: $DIR_COUNT, Orphan PVs: $ORPHAN_PV_COUNT, Orphan Dirs: $ORPHAN_DIR_COUNT"
echo

echo "=== [$HOST] Orphaned PVs (PV exists, directory missing) ==="
if [ -n "$ORPHAN_PVS" ]; then
    echo "$ORPHAN_PVS" | sed "s/^/  /"
else
    echo "  (none)"
fi
echo

echo "=== [$HOST] Orphaned directories (directory exists, no PV) ==="
if [ -n "$ORPHAN_DIRS" ]; then
    echo "$ORPHAN_DIRS" | sed "s|^|  $BASE_PATH/|"
else
    echo "  (none)"
fi
