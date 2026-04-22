#!/usr/bin/env bash
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
#
# Launch pmd3-bridge from source via uv, passing through env vars.
# Used by the integration / device test harness (🎯T26.4) to spin up
# the real bridge subprocess without requiring a PyInstaller build.
#
# Honours SPYDER_BRIDGE_FD_LIMIT (🎯T27): when set to an integer, the
# wrapper lowers the subprocess's RLIMIT_NOFILE via `ulimit -n` before
# exec'ing uv. Resource-leak regression tests set a tight limit so
# leaks surface within the test window rather than after hours.

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root/bridge"

if [[ -n "${SPYDER_BRIDGE_FD_LIMIT:-}" ]]; then
  ulimit -n "$SPYDER_BRIDGE_FD_LIMIT"
fi

# -q suppresses uv's informational chatter on stdout (would collide with
# the ready handshake line the Go supervisor is scanning for).
exec uv run --project . --quiet python -m pmd3_bridge "$@"
