#!/usr/bin/env bash
# Build the desktop stream player (player/) and copy the resulting binary
# to the given destination path. Used by release-package.sh to stage
# libexec/spyder/player/desktop/player (🎯T102).
set -euo pipefail
source "$(dirname "$0")/release-common.sh"

DEST="${1:?usage: package-player-desktop.sh <dest-path>}"

echo "release-package: make -C player" >&2
make -C "$ROOT/player" >&2

mkdir -p "$(dirname "$DEST")"
cp "$ROOT/bin/player" "$DEST"
chmod +x "$DEST"
