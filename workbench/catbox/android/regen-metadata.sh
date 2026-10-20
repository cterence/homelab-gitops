#!/usr/bin/env bash
# Regenerate gradle dependency verification metadata with the updated deps.
set -euo pipefail
cd "$(dirname "$0")/../../.."

export GRADLE_USER_HOME=/tmp/catbox-gradle-home
mkdir -p "$GRADLE_USER_HOME"

nix develop .#android -c bash -c '
  cd workbench/catbox/android
  command gradle --no-daemon --console=plain --write-verification-metadata=sha256 assembleDebug
'
