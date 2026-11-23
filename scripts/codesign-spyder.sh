#!/usr/bin/env bash
# codesign-spyder.sh — sign spyder CLI and packaged macOS binaries (🎯T133.1).
#
# Keychain ACLs bind to the code signature. Ad-hoc (`codesign -s -`) and
# unsigned Homebrew CDHash churn re-prompt on every rebuild. This script
# signs with a stable Developer ID Application (or fallback Apple Development)
# identity and forces the requested reverse-DNS identifier (default:
# com.marcelocantos.spyder).
#
# marcelocantos tap releases sign under Canticode Pty Ltd (N29CRQ669R).
# Override the team with SPYDER_CODESIGN_TEAM_ID if needed. (Squz/Minicades
# store-ship identities in internal/ship and player/ios are unchanged — this
# script covers spyder's CLI and packaged macOS helpers only.)
#
# Usage:
#   ./scripts/codesign-spyder.sh [path-to-binary] [identifier]
#   SPYDER_CODESIGN_IDENTITY="Developer ID Application: …" ./scripts/codesign-spyder.sh
set -euo pipefail

BIN="${1:-bin/spyder}"
BUNDLE_ID="${2:-com.marcelocantos.spyder}"
TEAM_ID="${SPYDER_CODESIGN_TEAM_ID:-N29CRQ669R}"

if [[ ! -f "$BIN" ]]; then
  echo "codesign-spyder: missing $BIN — run make build first" >&2
  exit 1
fi

cert_ou() {
  security find-certificate -c "$1" -p 2>/dev/null |
    openssl x509 -noout -subject 2>/dev/null |
    sed -n 's#.*OU *= *\([^,/]*\).*#\1#p'
}

pick_identity_for_team() {
  local team=$1 kind=$2 name ou id_list
  id_list=$(mktemp)
  trap 'rm -f "$id_list"' RETURN
  security find-identity -v -p codesigning 2>/dev/null |
    sed -n 's/^[[:space:]]*[0-9]*) [0-9A-F]* "\(.*\)"/\1/p' >"$id_list"
  while IFS= read -r name; do
    [[ "$name" == *"$kind"* ]] || continue
    ou=$(cert_ou "$name")
    [[ "$ou" == "$team" ]] || continue
    printf '%s\n' "$name"
    return 0
  done <"$id_list"
  return 1
}

pick_identity() {
  if [[ -n "${SPYDER_CODESIGN_IDENTITY:-}" ]]; then
    printf '%s\n' "$SPYDER_CODESIGN_IDENTITY"
    return
  fi
  local id
  id=$(pick_identity_for_team "$TEAM_ID" "Developer ID Application" || true)
  if [[ -n "$id" ]]; then
    printf '%s\n' "$id"
    return
  fi
  id=$(pick_identity_for_team "$TEAM_ID" "Apple Development" || true)
  if [[ -n "$id" ]]; then
    printf '%s\n' "$id"
    return
  fi
  echo "codesign-spyder: no Developer ID Application or Apple Development identity for team ${TEAM_ID}" >&2
  echo "  import a Canticode Pty Ltd (${TEAM_ID}) cert into the login keychain, or set SPYDER_CODESIGN_IDENTITY" >&2
  exit 1
}

IDENTITY="$(pick_identity)"
TIMESTAMP_OPT="--timestamp"
if [[ "$IDENTITY" == *"Apple Development"* ]]; then
  TIMESTAMP_OPT="--timestamp=none"
fi

echo "codesign-spyder: signing $BIN"
echo "  team:       ${TEAM_ID}"
echo "  identity:   $IDENTITY"
echo "  identifier: $BUNDLE_ID"
echo "  timestamp:  $TIMESTAMP_OPT"

codesign --force --sign "$IDENTITY" \
  --identifier "$BUNDLE_ID" \
  --options runtime \
  "$TIMESTAMP_OPT" \
  "$BIN"

codesign -dv --verbose=4 "$BIN" 2>&1 | grep -E '^(Identifier|Authority|TeamIdentifier|Signature|Timestamp)=' || true
echo "codesign-spyder: ok"
