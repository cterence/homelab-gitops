#!/bin/bash
# Compares Kubernetes PersistentVolumes (from local-path-provisioner) with the
# actual directories on disk under /opt/local-path-provisioner.
# Outputs three sections: orphaned PVs (no directory), orphaned dirs (no PV),
# and matched pairs.
#
# Usage: bash pv-diff.sh [BASE_PATH]
# Default BASE_PATH: /opt/local-path-provisioner

set -euo pipefail

BASE_PATH="${1:-/opt/local-path-provisioner}"

# Get PV directory names from local-path-provisioner
PV_NAMES=$(kubectl get pv -o json 2>/dev/null | jq -r '
  .items[]
  | select(.spec.local.path // "" | contains("/local-path-provisioner/"))
  | .spec.local.path | split("/") | last
' | sort)

# Get PV details for display
PV_DETAILS=$(kubectl get pv -o json 2>/dev/null | jq -r '
  .items[]
  | select(.spec.local.path // "" | contains("/local-path-provisioner/"))
  | "\(.spec.local.path | split("/") | last)\t\(.spec.claimRef.namespace // "?")/\(.spec.claimRef.name // "?")\t\(.status.phase // "?")"
')

# Get directories on disk
DIR_NAMES=$(ls -1 "$BASE_PATH" 2>/dev/null | sort)

echo "=== Kubernetes PVs (local-path-provisioner) ==="
echo "$PV_DETAILS" | column -t -s $'\t' 2>/dev/null || echo "(none)"
echo
echo "=== Directories on disk ($BASE_PATH) ==="
echo "$DIR_NAMES" | sed 's/^/  /'
echo

# Compare
ORPHAN_PVS=$(comm -23 <(echo "$PV_NAMES") <(echo "$DIR_NAMES"))
ORPHAN_DIRS=$(comm -13 <(echo "$PV_NAMES") <(echo "$DIR_NAMES"))
MATCHED=$(comm -12 <(echo "$PV_NAMES") <(echo "$DIR_NAMES"))

echo "=== Matched (PV has directory) ==="
if [ -n "$MATCHED" ]; then
    echo "$MATCHED" | sed 's/^/  /'
else
    echo "  (none)"
fi
echo

echo "=== Orphaned PVs (PV exists, directory missing) ==="
if [ -n "$ORPHAN_PVS" ]; then
    echo "$ORPHAN_PVS" | sed 's/^/  /'
else
    echo "  (none)"
fi
echo

echo "=== Orphaned directories (directory exists, no PV) ==="
if [ -n "$ORPHAN_DIRS" ]; then
    echo "$ORPHAN_DIRS" | sed "s|^|  $BASE_PATH/|"
else
    echo "  (none)"
fi
echo

# Summary
PV_COUNT=$(echo "$PV_NAMES" | grep -c . 2>/dev/null || echo 0)
DIR_COUNT=$(echo "$DIR_NAMES" | grep -c . 2>/dev/null || echo 0)
ORPHAN_PV_COUNT=$(echo "$ORPHAN_PVS" | grep -c . 2>/dev/null || echo 0)
ORPHAN_DIR_COUNT=$(echo "$ORPHAN_DIRS" | grep -c . 2>/dev/null || echo 0)
echo "=== Summary ==="
echo "  PVs: $PV_COUNT, Dirs: $DIR_COUNT, Orphan PVs: $ORPHAN_PV_COUNT, Orphan Dirs: $ORPHAN_DIR_COUNT"
