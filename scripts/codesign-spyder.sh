#!/usr/bin/env bash
# codesign-spyder.sh — sign bin/spyder as the studio-secrets principal (🎯T133.1).
#
# Keychain ACLs bind to the code signature. Ad-hoc (`codesign -s -`) and
# unsigned Homebrew CDHash churn re-prompt on every rebuild. This script
# signs with a stable Development or Developer ID Application identity and
# forces identifier com.marcelocantos.spyder.
#
# Usage:
#   ./scripts/codesign-spyder.sh [path-to-binary]
#   SPYDER_CODESIGN_IDENTITY="Apple Development: …" ./scripts/codesign-spyder.sh
#
# Prefer Developer ID Application when present; else Apple Development.
set -euo pipefail

BIN="${1:-bin/spyder}"
BUNDLE_ID="com.marcelocantos.spyder"

if [[ ! -f "$BIN" ]]; then
  echo "codesign-spyder: missing $BIN — run make build first" >&2
  exit 1
fi

pick_identity() {
  if [[ -n "${SPYDER_CODESIGN_IDENTITY:-}" ]]; then
    printf '%s\n' "$SPYDER_CODESIGN_IDENTITY"
    return
  fi
  # Prefer Developer ID Application (stable for Homebrew bottles).
  local id
  id=$(security find-identity -v -p codesigning 2>/dev/null \
    | sed -n 's/.*"\(Developer ID Application: .*\)"/\1/p' | head -1)
  if [[ -n "$id" ]]; then
    printf '%s\n' "$id"
    return
  fi
  # Fall back to a dedicated Apple Development identity.
  id=$(security find-identity -v -p codesigning 2>/dev/null \
    | sed -n 's/.*"\(Apple Development: .*\)"/\1/p' | head -1)
  if [[ -n "$id" ]]; then
    printf '%s\n' "$id"
    return
  fi
  echo "codesign-spyder: no Developer ID Application or Apple Development identity found" >&2
  echo "  import a cert into the login keychain, or set SPYDER_CODESIGN_IDENTITY" >&2
  exit 1
}

IDENTITY="$(pick_identity)"
echo "codesign-spyder: signing $BIN"
echo "  identity:   $IDENTITY"
echo "  identifier: $BUNDLE_ID"

codesign --force --sign "$IDENTITY" \
  --identifier "$BUNDLE_ID" \
  --options runtime \
  --timestamp=none \
  "$BIN"

codesign -dv --verbose=4 "$BIN" 2>&1 | egrep '^(Identifier|Authority|TeamIdentifier|Signature)=' || true
echo "codesign-spyder: ok"
