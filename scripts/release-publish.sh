#!/usr/bin/env bash
# Create the GitHub release from dist/, update the Homebrew tap, upgrade locally.
# Run after `make pre-release` and `scripts/release-package.sh`.
#
# After brew upgrade, restart the daemon with `supervisorctl restart spyder`
# on this Mac — not `brew services restart` (see CLAUDE.md).
set -euo pipefail
source "$(dirname "$0")/release-common.sh"

NOTES_FILE=""
SKIP_TAP=false
SKIP_BREW=false
VERSION_ARG=""

usage() {
	echo "usage: $0 [version] [--notes FILE] [--skip-tap] [--skip-brew]" >&2
	exit 2
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--notes)
		NOTES_FILE=$2
		shift 2
		;;
	--skip-tap)
		SKIP_TAP=true
		shift
		;;
	--skip-brew)
		SKIP_BREW=true
		shift
		;;
	-h | --help)
		usage
		;;
	-*)
		echo "unknown argument: $1" >&2
		usage
		;;
	*)
		if [[ -n "$VERSION_ARG" ]]; then
			echo "version already set to ${VERSION_ARG}" >&2
			usage
		fi
		VERSION_ARG=$1
		shift
		;;
	esac
done

VERSION="$(resolve_version "$VERSION_ARG")"
TAG="v${VERSION}"
ASSET="dist/spyder-${VERSION}-darwin-arm64.tar.gz"

require_gh

[[ -f "$ASSET" ]] || {
	echo "missing $ASSET — run scripts/release-package.sh ${VERSION} first" >&2
	exit 1
}

if gh release view "$TAG" >/dev/null 2>&1; then
	echo "release-publish: ${TAG} already exists on GitHub" >&2
	exit 1
fi

notes_tmp=""
if [[ -z "$NOTES_FILE" ]]; then
	notes_tmp="$(mktemp)"
	NOTES_FILE="$notes_tmp"
	if prev="$(git describe --tags --abbrev=0 2>/dev/null)"; then
		git log "${prev}..HEAD" --pretty=format:'- %s' >"$NOTES_FILE" || true
	fi
	[[ -s "$NOTES_FILE" ]] || echo "Release ${TAG}." >"$NOTES_FILE"
fi

trap 'rm -f "$notes_tmp"' EXIT

echo "release-publish: creating ${TAG} …" >&2
gh release create "$TAG" --title "$TAG" --notes-file "$NOTES_FILE" "$ASSET"
git fetch --tags

if [[ "$SKIP_TAP" == false ]]; then
	"$ROOT/scripts/release-tap.sh" "$TAG"
fi

if [[ "$SKIP_BREW" == false ]]; then
	echo "release-publish: brew upgrade …" >&2
	brew update
	brew upgrade marcelocantos/tap/spyder 2>/dev/null || brew install marcelocantos/tap/spyder
	got="$(spyder --version 2>/dev/null || true)"
	if [[ "$got" != "$TAG" && "$got" != "$VERSION" ]]; then
		echo "release-publish: expected spyder --version ${TAG}, got ${got:-<missing>}" >&2
		exit 1
	fi
	echo "release-publish: brew is ${got}; restart the daemon with supervisorctl restart spyder (not brew services)" >&2
fi

echo "release-publish: ${TAG} → https://github.com/marcelocantos/spyder/releases/tag/${TAG}"
