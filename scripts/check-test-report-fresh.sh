#!/usr/bin/env bash
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
#
# Verify TEST-REPORT.json attests the current HEAD (🎯T132.2 / ENT-002).
# Used by make pre-release and scripts/hooks/pre-push.

set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v jq >/dev/null 2>&1; then
  echo "check-test-report-fresh: jq is required (brew install jq)." >&2
  exit 1
fi

report=TEST-REPORT.json
if [[ ! -f "$report" ]]; then
  echo "check-test-report-fresh: missing $report (run make test-report)" >&2
  exit 1
fi

overall=$(jq -r '.overall // empty' "$report")
if [[ "$overall" == fail ]]; then
  echo "check-test-report-fresh: $report overall=$overall (a suite failed)" >&2
  exit 1
fi
if [[ "$overall" != pass && "$overall" != partial ]]; then
  echo "check-test-report-fresh: $report overall=$overall (want pass or partial)" >&2
  exit 1
fi

head_sha=$(git rev-parse HEAD)
report_sha=$(jq -r '.head_sha // empty' "$report")
if [[ -z "$report_sha" ]]; then
  echo "check-test-report-fresh: $report has no head_sha (re-run make test-report on this commit)" >&2
  exit 1
fi

if ! git cat-file -e "${report_sha}^{commit}" 2>/dev/null; then
  echo "check-test-report-fresh: $report head_sha $report_sha is not a reachable commit" >&2
  exit 1
fi

if ! git merge-base --is-ancestor "$report_sha" "$head_sha"; then
  echo "check-test-report-fresh: report SHA $report_sha is not an ancestor of HEAD $head_sha" >&2
  exit 1
fi

source_paths=(
  internal/
  bridge/src/
  bridge/tests/
  main.go
  Makefile
  go.mod
  go.sum
  bridge/pyproject.toml
  bridge/uv.lock
  scripts/
)
for f in ./*.go; do
  [[ -e "$f" ]] && source_paths+=("$f")
done

if ! git diff --quiet "$report_sha..$head_sha" -- "${source_paths[@]}"; then
  echo "check-test-report-fresh: source changed since report head_sha $report_sha" >&2
  echo "  Re-run: make test-report && git add TEST-REPORT.json && git commit" >&2
  git diff --stat "$report_sha..$head_sha" -- "${source_paths[@]}" >&2 || true
  exit 1
fi

echo "check-test-report-fresh: ok (head_sha=$report_sha)"
