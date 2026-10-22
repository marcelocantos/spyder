#!/usr/bin/env bash
# Compare stream wire magics in spyder's copied Protocol.h with ge's encoder
# header when a ge checkout is available (ENT-006 / 🎯T132.6). Skips closed
# when ge is absent — the local sizeof oracle still runs via check-wire.
#
# Override the ge tree with SPYDER_GE_ROOT (repo root containing include/ge/).
set -euo pipefail

root="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
spyder_protocol="$root/player/include/wire/Protocol.h"
ge_root="${SPYDER_GE_ROOT:-$root/../ge}"
ge_protocol="$ge_root/include/ge/Protocol.h"

if [[ ! -f "$spyder_protocol" ]]; then
  echo "check-wire-ge: missing $spyder_protocol" >&2
  exit 1
fi

if [[ ! -f "$ge_protocol" ]]; then
  echo "check-wire-ge: skip (no ge Protocol.h at $ge_protocol; set SPYDER_GE_ROOT to override)"
  exit 0
fi

extract_magics() {
  grep -E 'constexpr uint32_t k[A-Za-z0-9_]*Magic\s*=' "$1" \
    | sed -E 's/[[:space:]]+//g;s/;//' \
    | sort
}

spyder_magics="$(extract_magics "$spyder_protocol")"
ge_magics="$(extract_magics "$ge_protocol")"

if [[ "$spyder_magics" != "$ge_magics" ]]; then
  echo "check-wire-ge FAIL: magic constants differ between spyder and ge Protocol.h" >&2
  diff -u <(printf '%s\n' "$spyder_magics") <(printf '%s\n' "$ge_magics") >&2 || true
  exit 1
fi

norm_version() {
  grep -E 'constexpr uint16_t kProtocolVersion' "$1" | sed -E 's/[[:space:]]+//g;s/;//'
}

spyder_ver="$(norm_version "$spyder_protocol")"
ge_ver="$(norm_version "$ge_protocol")"
if [[ "$spyder_ver" != "$ge_ver" ]]; then
  echo "check-wire-ge FAIL: kProtocolVersion differs (spyder: $spyder_ver ge: $ge_ver)" >&2
  exit 1
fi

magic_count="$(printf '%s\n' "$spyder_magics" | grep -c . || true)"
echo "check-wire-ge OK ($magic_count magics, $spyder_ver, ge=$ge_root)"
