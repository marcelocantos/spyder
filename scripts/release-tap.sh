#!/usr/bin/env bash
# Update the Homebrew tap declared in tapper.yaml for an existing GitHub release.
set -euo pipefail
source "$(dirname "$0")/release-common.sh"

TAG="$(release_tag "${1:-}")"
command -v tapper >/dev/null || {
	echo "tapper not on PATH — brew install marcelocantos/tap/tapper" >&2
	exit 1
}
exec tapper push --version "$TAG"
