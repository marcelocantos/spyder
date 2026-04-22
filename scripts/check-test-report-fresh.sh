#!/usr/bin/env bash
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
#
# Verify TEST-REPORT.json is fresh and passing (🎯T26.4).
#
# Shared between the pre-push hook and the `test-report-fresh` bullseye
# invariant so local and CI enforcement agree on a single definition of
# freshness.

set -euo pipefail

cd "$(dirname "$0")/.."

die() { echo "test-report: $*" >&2; exit 1; }

[[ -f TEST-REPORT.json ]] \
  || die "TEST-REPORT.json missing. Run: make test-report"

if ! overall=$(jq -er .overall TEST-REPORT.json 2>/dev/null); then
  die "TEST-REPORT.json unreadable."
fi
case "$overall" in
  pass)
    ;;
  partial)
    # Acceptable for routine pushes — tiers requiring devices or flags are
    # often genuinely skipped. Surface what was skipped so the developer
    # knows what coverage they have.
    skipped=$(jq -r '[.suites[] | select(.status == "skipped") | .name] | join(",")' TEST-REPORT.json)
    echo "note: TEST-REPORT.json overall=partial; skipped: $skipped" >&2
    ;;
  fail)
    die "TEST-REPORT.json overall=fail; fix failing tests and re-run: make test-report"
    ;;
  *)
    die "TEST-REPORT.json has unexpected overall=$overall"
    ;;
esac

report_sha=$(jq -r .commit TEST-REPORT.json)
git cat-file -e "$report_sha" 2>/dev/null \
  || die "TEST-REPORT.json references unknown commit $report_sha."

# Source paths that invalidate the report when they differ between the
# recorded SHA and HEAD. docs/, README.md, CLAUDE.md, targets.md are
# intentionally not in the list.
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

if ! git diff --quiet "$report_sha"..HEAD -- "${source_paths[@]}"; then
  echo "test-report: source changed since last test run ($report_sha..HEAD):" >&2
  git diff --stat "$report_sha"..HEAD -- "${source_paths[@]}" >&2
  die "run: make test-report"
fi

echo "✓ test-report fresh ($report_sha, overall=$overall)"
