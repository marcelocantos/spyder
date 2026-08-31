#!/usr/bin/env bash
# codesign-spyder.sh — sign bin/spyder as the studio-secrets principal (🎯T133.1).
#
# Keychain ACLs bind to the code signature. Ad-hoc (`codesign -s -`) and
# unsigned Homebrew CDHash churn re-prompt on every rebuild. This script
# signs with a stable Development or Developer ID Application identity and
# forces identifier com.marcelocantos.spyder.
#
# marcelocantos tap releases sign under Squz (SWA3H3N7TW) until the
# canticode.com Apple Developer Program account is active. Override the team
# with SPYDER_CODESIGN_TEAM_ID when switching. (Squz/Minicades store-ship
# identities in internal/ship are unchanged — this script covers the CLI only.)
#
# Usage:
#   ./scripts/codesign-spyder.sh [path-to-binary]
#   SPYDER_CODESIGN_IDENTITY="Apple Development: …" ./scripts/codesign-spyder.sh
set -euo pipefail

BIN="${1:-bin/spyder}"
BUNDLE_ID="com.marcelocantos.spyder"
TEAM_ID="${SPYDER_CODESIGN_TEAM_ID:-SWA3H3N7TW}"

if [[ ! -f "$BIN" ]]; then
  echo "codesign-spyder: missing $BIN — run make build first" >&2
  exit 1
fi

cert_ou() {
  security find-certificate -c "$1" -p 2>/dev/null |
    openssl x509 -noout -subject 2>/dev/null |
    sed -n 's/.*OU=\([^,]*\).*/\1/p'
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
  echo "  import a Squz cert into the login keychain, or set SPYDER_CODESIGN_IDENTITY" >&2
  echo "  (interim: SWA3H3N7TW until canticode.com Developer Program is active)" >&2
  exit 1
}

IDENTITY="$(pick_identity)"
echo "codesign-spyder: signing $BIN"
echo "  team:       ${TEAM_ID}"
echo "  identity:   $IDENTITY"
echo "  identifier: $BUNDLE_ID"

codesign --force --sign "$IDENTITY" \
  --identifier "$BUNDLE_ID" \
  --options runtime \
  --timestamp=none \
  "$BIN"

codesign -dv --verbose=4 "$BIN" 2>&1 | egrep '^(Identifier|Authority|TeamIdentifier|Signature)=' || true
echo "codesign-spyder: ok"
