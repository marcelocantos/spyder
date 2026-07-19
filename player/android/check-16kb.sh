#!/usr/bin/env bash
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
#
# Fail if any arm64-v8a .so in the player APK has a PT_LOAD Align other than
# 0x4000 (16 KB). 🎯T75 contract for the standalone tools/android player path
# (does not include cmake/android-arm64.cmake — must not regress to 4 KB).
#
# Usage (from tools/android/ after assembleDebug):
#   ./check-16kb.sh
#   ./check-16kb.sh app/build/outputs/apk/debug/app-debug.apk
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
APK="${1:-$ROOT/app/build/outputs/apk/debug/app-debug.apk}"
if [[ ! -f "$APK" ]]; then
  echo "check-16kb: APK not found: $APK" >&2
  exit 2
fi

NDK_ROOT="${ANDROID_NDK_ROOT:-${ANDROID_NDK_HOME:-}}"
if [[ -z "$NDK_ROOT" ]]; then
  SDK="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
  NDK_ROOT=$(ls -d "$SDK"/ndk/* 2>/dev/null | sort -V | tail -1 || true)
fi
READELF=$(find "${NDK_ROOT:-/nonexistent}" -path '*/bin/llvm-readelf' 2>/dev/null | head -1)
if [[ -z "$READELF" || ! -x "$READELF" ]]; then
  echo "check-16kb: llvm-readelf not found (set ANDROID_NDK_ROOT)" >&2
  exit 2
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
unzip -q -o "$APK" 'lib/arm64-v8a/*' -d "$tmpdir"
shopt -s nullglob
sos=("$tmpdir"/lib/arm64-v8a/*.so)
if ((${#sos[@]} == 0)); then
  echo "check-16kb: no lib/arm64-v8a/*.so in $APK" >&2
  exit 1
fi

bad=0
for so in "${sos[@]}"; do
  name=$(basename "$so")
  so_bad=0
  # Last field of llvm-readelf -lW LOAD lines is Align.
  while read -r align; do
    if [[ "$align" != "0x4000" ]]; then
      echo "check-16kb: FAIL $name PT_LOAD Align=$align (want 0x4000)" >&2
      so_bad=1
      bad=1
    fi
  done < <("$READELF" -lW "$so" | awk '/LOAD/ {print $NF}')
  if [[ $so_bad -eq 0 ]]; then
    echo "check-16kb: OK $name (all PT_LOAD Align 0x4000)"
  fi
done
# libc++_shared must not reappear under c++_static builds — it was the
# third 4 KB lib in the Android 16 system dialog.
if [[ -f "$tmpdir/lib/arm64-v8a/libc++_shared.so" ]]; then
  echo "check-16kb: FAIL libc++_shared.so packaged (use ANDROID_STL=c++_static)" >&2
  bad=1
fi
exit $bad
