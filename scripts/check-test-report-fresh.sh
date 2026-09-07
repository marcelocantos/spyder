#!/usr/bin/env bash
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
#
# Fail when TEST-REPORT.json's attestation predates the code it claims to
# vouch for.
#
# scripts/test-report.sh says so in its own header: "nothing in this script
# or the pre-push hook checks freshness against git history." So the report
# could sit at a two-week-old timestamp across dozens of Go commits while
# every consumer read it as "tests passed on this tree". This is that
# missing check; `make bullseye` runs it.
#
# Staleness is judged against commits, not a SHA: a squash-merge rewrites
# the SHA the report was generated at, but the committer dates of the
# commits that touch testable code still order correctly against
# generated_at.

set -euo pipefail

cd "$(dirname "$0")/.."

REPORT=TEST-REPORT.json

# Paths whose change invalidates the attestation: the code under test, the
# dependency set, and the harness that produces the report.
TESTABLE_PATHS=('*.go' 'go.mod' 'go.sum' 'scripts/test-report.sh')

if ! command -v jq >/dev/null 2>&1; then
  echo "check-test-report-fresh: jq is required (brew install jq)." >&2
  exit 1
fi

if [[ ! -f "$REPORT" ]]; then
  echo "check-test-report-fresh: $REPORT is missing. Run: make test-report" >&2
  exit 1
fi

generated_at=$(jq -r '.generated_at // empty' "$REPORT")
if [[ -z "$generated_at" ]]; then
  echo "check-test-report-fresh: $REPORT has no generated_at. Run: make test-report" >&2
  exit 1
fi

overall=$(jq -r '.overall // empty' "$REPORT")
if [[ "$overall" == "fail" ]]; then
  echo "check-test-report-fresh: $REPORT records overall=fail." >&2
  jq -r '.suites[] | select(.status == "fail") | "  ✗ \(.name): \(.cmd)"' "$REPORT" >&2
  exit 1
fi

# Committed drift. `git log --since` reads ISO-8601 natively, which keeps
# this off the BSD/GNU `date` parsing fault line.
newer=$(git log --since="$generated_at" --oneline -- "${TESTABLE_PATHS[@]}")

# Uncommitted drift. The report is only ever generated on a clean tree, so
# any modified testable file also postdates it.
dirty=$(git status --porcelain -- "${TESTABLE_PATHS[@]}")

if [[ -z "$newer" && -z "$dirty" ]]; then
  echo "✓ TEST-REPORT.json is current (generated_at=$generated_at, overall=$overall)"
  exit 0
fi

echo "✗ TEST-REPORT.json is stale: generated_at=$generated_at predates the code it attests to." >&2
if [[ -n "$newer" ]]; then
  echo "  commits touching testable paths since then:" >&2
  echo "$newer" | sed 's/^/    /' >&2
fi
if [[ -n "$dirty" ]]; then
  echo "  uncommitted changes to testable paths:" >&2
  echo "$dirty" | sed 's/^/    /' >&2
fi
echo "  Regenerate on a clean tree: make test-report" >&2
exit 1
