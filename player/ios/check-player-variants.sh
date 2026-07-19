#!/usr/bin/env bash
# Structural oracle for 🎯T154.3: three player packaging variants exist with
# the correct UISupportedInterfaceOrientations inventories.
# Usage: tools/ios/check-player-variants.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
fail=0

check_plist() {
  local file="$1" expect="$2" label="$3"
  if [[ ! -f "$ROOT/$file" ]]; then
    echo "FAIL: missing $file ($label)"
    fail=1
    return
  fi
  local orients
  orients=$(plutil -extract UISupportedInterfaceOrientations xml1 -o - "$ROOT/$file" 2>/dev/null \
    | rg -o 'UIInterfaceOrientation[A-Za-z]+' | sort | tr '\n' ' ')
  # shellcheck disable=SC2086
  if [[ "$orients" != "$expect" ]]; then
    echo "FAIL: $file orientations='$orients' expected='$expect' ($label)"
    fail=1
  else
    echo "OK: $file → $orients($label)"
  fi
}

# Trailing space from tr
check_plist Info.plist \
  "UIInterfaceOrientationLandscapeLeft UIInterfaceOrientationLandscapeRight UIInterfaceOrientationPortrait UIInterfaceOrientationPortraitUpsideDown " \
  "Player don't-care"
check_plist Info-Land.plist \
  "UIInterfaceOrientationLandscapeLeft UIInterfaceOrientationLandscapeRight " \
  "PlayerLand"
check_plist Info-Port.plist \
  "UIInterfaceOrientationPortrait UIInterfaceOrientationPortraitUpsideDown " \
  "PlayerPort"

if ! rg -q 'add_player_variant\(Player ' "$ROOT/CMakeLists.txt"; then
  echo "FAIL: CMakeLists missing Player variant"
  fail=1
fi
if ! rg -q 'add_player_variant\(PlayerLand ' "$ROOT/CMakeLists.txt"; then
  echo "FAIL: CMakeLists missing PlayerLand variant"
  fail=1
fi
if ! rg -q 'add_player_variant\(PlayerPort ' "$ROOT/CMakeLists.txt"; then
  echo "FAIL: CMakeLists missing PlayerPort variant"
  fail=1
fi
if ! rg -q 'com\.squz\.player\.land' "$ROOT/CMakeLists.txt"; then
  echo "FAIL: CMakeLists missing com.spyder.player.land"
  fail=1
fi
if ! rg -q 'com\.squz\.player\.port' "$ROOT/CMakeLists.txt"; then
  echo "FAIL: CMakeLists missing com.spyder.player.port"
  fail=1
fi
echo "OK: CMakeLists three targets + bundle IDs"

# Display names (launcher titles)
for f_label in "Info.plist:Player" "Info-Land.plist:PlayerLand" "Info-Port.plist:PlayerPort"; do
  f="${f_label%%:*}"; want="${f_label##*:}"
  got=$(plutil -extract CFBundleDisplayName raw "$ROOT/$f" 2>/dev/null || true)
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $f CFBundleDisplayName='$got' expected='$want'"
    fail=1
  else
    echo "OK: $f display name $got"
  fi
done

# Icon assets (base + L / P badged variants)
for assets in Assets-Player.xcassets Assets-PlayerLand.xcassets Assets-PlayerPort.xcassets; do
  if [[ ! -f "$ROOT/$assets/AppIcon.appiconset/icon.png" ]]; then
    echo "FAIL: missing $assets/AppIcon.appiconset/icon.png"
    fail=1
  else
    echo "OK: $assets has AppIcon"
  fi
done
if ! rg -q 'Assets-PlayerLand.xcassets' "$ROOT/CMakeLists.txt"; then
  echo "FAIL: CMakeLists not wiring Assets-PlayerLand"
  fail=1
fi

exit "$fail"
