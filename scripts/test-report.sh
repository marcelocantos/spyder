#!/usr/bin/env bash
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
#
# Generate TEST-REPORT.json by running every test tier on the current
# (clean-tree) HEAD and recording per-suite outcomes (🎯T26.4).
#
# Workflow: commit → make test-report → `git commit -m "TEST-REPORT.json
# @ <sha>" TEST-REPORT.json` to add the report as a separate commit on
# top of the one it vouches for.
#
# Don't `git commit --amend` to fold it in: the amend creates a new
# parent SHA, but TEST-REPORT.json's recorded `commit` field still
# points at the pre-amend SHA, which is orphaned and unreachable on
# the remote. Local pre-push works (orphan is in the reflog) but CI
# (which clones fresh) fails the freshness check with "references
# unknown commit". A separate commit keeps the recorded SHA reachable
# from the branch tip and the source-path diff between SHA and HEAD
# stays empty (only TEST-REPORT.json changed).

set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v jq >/dev/null 2>&1; then
  echo "test-report: jq is required (brew install jq)." >&2
  exit 1
fi

# Refuse to run on a dirty tree. The report's meaning is "these tests ran
# against this exact commit"; a dirty tree makes that claim incoherent.
if [[ -n "$(git status --porcelain)" ]]; then
  echo "test-report: refusing to run on a dirty tree. Commit or stash first:" >&2
  git status --short >&2
  exit 1
fi

commit=$(git rev-parse HEAD)
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

# ── Tier 1: Go unit ──────────────────────────────────────────────────────────
run_suite go-unit "go test ./..."

# ── Tier 1: Python bridge unit ───────────────────────────────────────────────
run_suite bridge-python-unit "cd bridge && uv run --project . --extra dev pytest tests/"

# ── Tier 2: integration against real bridge subprocess ───────────────────────
# Gated on INTEGRATION=1 until the harness lands (🎯T26.4 part 3).
if [[ "${SPYDER_INTEGRATION:-0}" == "1" ]]; then
  run_suite integration "go test -tags=integration ./internal/pmd3bridge/..."
else
  run_suite_skipped integration "set SPYDER_INTEGRATION=1 to run (harness lands incrementally)"
fi

# ── Tier 3: device tier ──────────────────────────────────────────────────────
# Gated on SPYDER_DEVICES=1 and at least one attached device.
if [[ "${SPYDER_DEVICES:-0}" == "1" ]]; then
  run_suite device "go test -tags=device ./internal/pmd3bridge/..."
else
  run_suite_skipped device "set SPYDER_DEVICES=1 to run; requires a paired device"
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
  --arg schema_version 1 \
  --arg commit "$commit" \
  --arg generated_at "$generated_at" \
  --arg host "$host" \
  --argjson suites "$suites_json" \
  --arg overall "$overall" \
  '{
    schema_version: ($schema_version | tonumber),
    commit: $commit,
    generated_at: $generated_at,
    host: $host,
    suites: $suites,
    overall: $overall
  }' > TEST-REPORT.json

echo "── test-report: overall=$overall → TEST-REPORT.json ──"
echo "Next: git commit -m \"TEST-REPORT.json @ ${commit:0:7}\" TEST-REPORT.json"
