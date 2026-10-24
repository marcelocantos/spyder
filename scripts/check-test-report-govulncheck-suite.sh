#!/usr/bin/env bash
# Regression: test-report.sh wires optional govulncheck (ENT-012 / 🎯T132.12).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tr="$root/scripts/test-report.sh"

grep -q 'command -v govulncheck' "$tr"
grep -q 'run_suite govulncheck' "$tr"
grep -q 'run_suite_skipped govulncheck' "$tr"
grep -q 'GOWORK=off govulncheck ./\.\.\.' "$tr"

echo 'check-test-report-govulncheck-suite: PASS'
