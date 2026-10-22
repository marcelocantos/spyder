#!/usr/bin/env bash
# Regression fixture for check-wire-ge.sh (match, mismatch, skip-without-ge).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
spyder_protocol="$root/player/include/wire/Protocol.h"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

ge="$fixture/ge/include/ge"
mkdir -p "$ge"

# Match: mirror spyder magics + version into a disposable ge checkout.
{
  grep -E 'constexpr uint32_t k[A-Za-z0-9_]*Magic\s*=' "$spyder_protocol"
  grep -E 'constexpr uint16_t kProtocolVersion' "$spyder_protocol"
} >"$ge/Protocol.h"

if ! SPYDER_GE_ROOT="$fixture/ge" "$root/scripts/check-wire-ge.sh" >/dev/null; then
  echo "check-wire-ge-test: expected match PASS" >&2
  exit 1
fi

echo 'constexpr uint32_t kOtherMagic = 0x1u;' >>"$ge/Protocol.h"
if SPYDER_GE_ROOT="$fixture/ge" "$root/scripts/check-wire-ge.sh" >/dev/null 2>&1; then
  echo "check-wire-ge-test: expected mismatch FAIL" >&2
  exit 1
fi

if ! SPYDER_GE_ROOT="$fixture/missing-ge" "$root/scripts/check-wire-ge.sh" >/dev/null; then
  echo "check-wire-ge-test: expected skip when ge absent" >&2
  exit 1
fi

echo 'check-wire-ge fixture PASS'
