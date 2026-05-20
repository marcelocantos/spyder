#!/usr/bin/env bash
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
#
# Generate TEST-REPORT.json by running every test tier on the current
# tree and recording per-suite outcomes (🎯T26.4).
#
# The report is an attestation: "the engineer ran these tests, here is
# how each tier went." Keeping the report up to date relative to the
# code is the engineer's responsibility; nothing in this script or the
# pre-push hook checks freshness against git history.

set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v jq >/dev/null 2>&1; then
  echo "test-report: jq is required (brew install jq)." >&2
  exit 1
fi

# Refuse to run on a dirty tree. The report's meaning is "these tests
# ran against this exact tree"; a dirty tree makes that claim incoherent.
# Exception: TEST-REPORT.json itself, since we're about to overwrite it
# and the prior contents (from an earlier completed or killed run)
# don't reflect anything about the tree we're testing.
dirty=$(git status --porcelain | grep -v -E '^.. TEST-REPORT\.json$' || true)
if [[ -n "$dirty" ]]; then
  echo "test-report: refusing to run on a dirty tree. Commit or stash first:" >&2
  echo "$dirty" >&2
  exit 1
fi

host=$(hostname -s)
generated_at=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

suite_results=()

run_suite() {
  local name="$1" cmd="$2"
  local started status duration
  echo "── test-report: running $name ──"
  started=$(date +%s)
  if bash -c "$cmd"; then
    status=pass
  else
    status=fail
  fi
  duration=$(($(date +%s) - started))
  local entry
  entry=$(jq -n \
    --arg name "$name" \
    --arg cmd "$cmd" \
    --argjson duration_s "$duration" \
    --arg status "$status" \
    '{name: $name, cmd: $cmd, duration_s: $duration_s, status: $status}')
  suite_results+=("$entry")
  echo "── test-report: $name → $status (${duration}s) ──"
  echo
}

run_suite_skipped() {
  local name="$1" reason="$2"
  local entry
  entry=$(jq -n \
    --arg name "$name" \
    --arg reason "$reason" \
    '{name: $name, status: "skipped", reason: $reason}')
  suite_results+=("$entry")
  echo "── test-report: $name → skipped ($reason) ──"
  echo
}

# Per-suite extra flags (e.g. -v for streamed test names + log output).
# Set TEST_FLAGS in the environment to add to every `go test` invocation.
GO_TEST_FLAGS="${TEST_FLAGS:-}"

# Per-suite timeout. A complete run takes ~40s on this host, so 2m is
# plenty of margin without being stuck for 10 minutes (Go's default)
# when a device-side hang takes a test out. -timeout goes before
# TEST_FLAGS so a caller can override with e.g. TEST_FLAGS='-timeout 10m'.
DEFAULT_TIMEOUT="-timeout 2m"

# ── Tier 1: Go unit ──────────────────────────────────────────────────────────
run_suite go-unit "go test $DEFAULT_TIMEOUT $GO_TEST_FLAGS ./..."

# ── Tier 2: live device tier (go-ios) ────────────────────────────────────────
# Gated on SPYDER_LIVE_UDID. Requires a paired iOS device + the bundled
# `ios tunnel start --userspace` running (spyder spawns it; outside spyder
# you can run `bin/ios tunnel start --userspace` manually).
if [[ -n "${SPYDER_LIVE_UDID:-}" ]]; then
  run_suite live "go test $DEFAULT_TIMEOUT $GO_TEST_FLAGS -run '_Live$' ./internal/device/..."
else
  run_suite_skipped live "set SPYDER_LIVE_UDID=<udid> to run; requires a paired device"
fi

# ── Compose overall ──────────────────────────────────────────────────────────
# pass   = every suite passed
# partial = at least one suite skipped, none failed
# fail   = any suite failed
suites_json=$(printf '%s\n' "${suite_results[@]}" | jq -s '.')
any_fail=$(echo "$suites_json" | jq '[.[] | select(.status == "fail")] | length')
any_skip=$(echo "$suites_json" | jq '[.[] | select(.status == "skipped")] | length')
if [[ "$any_fail" != "0" ]]; then
  overall=fail
elif [[ "$any_skip" != "0" ]]; then
  overall=partial
else
  overall=pass
fi

jq -n \
  --arg schema_version 2 \
  --arg generated_at "$generated_at" \
  --arg host "$host" \
  --argjson suites "$suites_json" \
  --arg overall "$overall" \
  '{
    schema_version: ($schema_version | tonumber),
    generated_at: $generated_at,
    host: $host,
    suites: $suites,
    overall: $overall
  }' > TEST-REPORT.json

echo "── test-report: overall=$overall → TEST-REPORT.json ──"
