#!/usr/bin/env bash
# Puts the catbox binary into the app's jniLibs for the devshell
# gradle build (nix develop .#android). The canonical APK build is
# `nix build .#catbox-android`, which wires the binary in itself.
set -euo pipefail
cd "$(dirname "$0")/.."

out=$(nix build --no-link --print-out-paths "$(dirname "$0")/../..#catbox-android-bin")
install -D "$out/lib/arm64-v8a/libcatbox.so" \
  android/app/src/main/jniLibs/arm64-v8a/libcatbox.so
echo "installed: android/app/src/main/jniLibs/arm64-v8a/libcatbox.so"
