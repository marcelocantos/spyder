#!/usr/bin/env bash
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
#
# Launch pmd3-bridge from source via uv, passing through env vars.
# Used by the integration test harness (🎯T26.4) to spin up the real
# bridge subprocess without requiring a PyInstaller build.
#
# Runs uv's own output on stderr (inherited) and exposes the bridge's
# stdout (the `ready port=… token=…` handshake line) on our stdout.

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root/bridge"

# -q suppresses uv's informational chatter on stdout (would collide with
# the ready handshake line the Go supervisor is scanning for).
exec uv run --project . --quiet python -m pmd3_bridge "$@"
