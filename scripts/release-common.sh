#!/usr/bin/env bash
# Shared helpers for local release (package, publish, tap).
set -euo pipefail

export GOWORK=off

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Strip an optional leading v. Empty stays empty.
normalize_version() {
	local v=$1
	v=${v#v}
	printf '%s' "$v"
}

_check_version() {
	local v=$1 hint=$2
	if [[ -z "$v" ]]; then
		echo "version required ($hint)" >&2
		exit 2
	fi
	if [[ ! "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-].*)?$ ]]; then
		echo "refusing version '$v': expected N.N.N" >&2
		exit 2
	fi
	printf '%s' "$v"
}

# VERSION from $1, else RELEASE_VERSION. Used by release-package.sh so a
# leftover dist/ tarball cannot silently become the next cut.
require_version() {
	local v
	v=$(normalize_version "${1:-}")
	if [[ -z "$v" ]]; then
		v=$(normalize_version "${RELEASE_VERSION:-}")
	fi
	_check_version "$v" "arg or RELEASE_VERSION; package never infers from dist/"
}

# VERSION from $1, else RELEASE_VERSION, else the darwin-arm64 tarball in dist/.
resolve_version() {
	local v raw f
	v=$(normalize_version "${1:-}")
	if [[ -z "$v" ]]; then
		v=$(normalize_version "${RELEASE_VERSION:-}")
	fi
	if [[ -z "$v" ]]; then
		f=""
		for raw in dist/spyder-*-darwin-arm64.tar.gz; do
			[[ -f "$raw" ]] || continue
			f=$(basename "$raw")
			break
		done
		if [[ -n "$f" ]]; then
			v=${f#spyder-}
			v=${v%-darwin-arm64.tar.gz}
		fi
	fi
	_check_version "$v" "arg, RELEASE_VERSION, or dist/spyder-*-darwin-arm64.tar.gz"
}

release_tag() {
	printf 'v%s' "$(resolve_version "${1:-}")"
}

require_gh() {
	command -v gh >/dev/null || {
		echo "gh CLI required" >&2
		exit 1
	}
	gh auth status >/dev/null 2>&1 || {
		echo "gh auth login required" >&2
		exit 1
	}
}
