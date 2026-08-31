#!/bin/sh
# Start spyder serve for supervisord. Homebrew Cellar only on :3030 —
# a tree-built binary on that port is the double-serve failure in CLAUDE.md.
set -e

if [ -z "${HOME:-}" ]; then
  HOME="$(eval echo ~"$(id -un)")"
  export HOME
fi
export USER="${USER:-$(id -un)}"
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:${HOME}/.cargo/bin:${HOME}/.local/bin:${HOME}/.py/bin:${HOME}/go/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export SPYDER_ADDR="${SPYDER_ADDR:-:3030}"

if [ -n "${SPYDER_BIN:-}" ]; then
  BIN="$SPYDER_BIN"
  if [ ! -x "$BIN" ]; then
    echo "spyder: SPYDER_BIN=$BIN is not executable" >&2
    exit 1
  fi
else
  BIN="$(command -v spyder 2>/dev/null || true)"
  if [ -z "$BIN" ] && command -v brew >/dev/null 2>&1; then
    BIN="$(brew --prefix)/opt/spyder/bin/spyder"
  fi
  if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
    echo "spyder: no spyder on PATH and no Homebrew install found." >&2
    echo "spyder: brew install marcelocantos/tap/spyder" >&2
    exit 1
  fi
fi

if [ "${1:-}" = "--print-bin" ]; then
  echo "$BIN"
  exit 0
fi

echo "spyder: running $BIN serve (SPYDER_ADDR=$SPYDER_ADDR)" >&2
exec "$BIN" serve
