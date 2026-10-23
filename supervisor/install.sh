#!/bin/sh
# Render supervisor/spyder.ini into supervisor.d and make supervisord
# the owner of :3030. Exactly one parent may own that port — this
# installer evicts brew services / launchd before starting (same
# takeover as jevons supervisor/install.sh).
set -e

REPO="$(CDPATH= cd "$(dirname "$0")/.." && pwd)"
CONF_DIR="${SUPERVISOR_CONF_DIR:-/opt/homebrew/etc/supervisor.d}"
DEST="$CONF_DIR/spyder.ini"
TEMPLATE="$REPO/supervisor/spyder.ini"

if [ -z "${HOME:-}" ]; then
  HOME="$(eval echo ~"$(id -un)")"
  export HOME
fi

mkdir -p "$CONF_DIR"
mkdir -p "$HOME/.local/var/log"
chmod +x "$REPO/supervisor/run-spyder.sh" "$REPO/supervisor/install.sh"

rm -f "$DEST"
# Resident iOS agent backend (🎯T175). Per-host UDIDs come from the
# installer's environment, never from the template.
IOS_AGENT_DEVICES="${SPYDER_IOS_AGENT_DEVICES:-}"
case "$IOS_AGENT_DEVICES" in
  *[!0-9A-Fa-f,*-]*) echo "SPYDER_IOS_AGENT_DEVICES must be UDIDs separated by commas, or *" >&2; exit 1 ;;
esac
if [ -n "$IOS_AGENT_DEVICES" ]; then IOS_AGENT_BACKEND=devicekit; else IOS_AGENT_BACKEND=alertrunner; fi
sed -e "s|@REPO@|$REPO|g" -e "s|@IOS_AGENT_BACKEND@|$IOS_AGENT_BACKEND|g" \
  -e "s|@IOS_AGENT_DEVICES@|$IOS_AGENT_DEVICES|g" "$TEMPLATE" >"$DEST"
echo "rendered $DEST (from $TEMPLATE)"

if [ "${SUPERVISOR_SKIP_CTL:-}" = 1 ]; then
  exit 0
fi

if ! command -v supervisorctl >/dev/null 2>&1; then
  echo "supervisorctl not on PATH — ini written; start Homebrew supervisor to load it" >&2
  exit 1
fi

# Evict launchd / brew services so they cannot reclaim :3030.
if command -v brew >/dev/null 2>&1; then
  brew services stop spyder >/dev/null 2>&1 || true
fi
if command -v launchctl >/dev/null 2>&1; then
  launchctl bootout "gui/$(id -u)/homebrew.mxcl.spyder" 2>/dev/null || true
fi

if command -v lsof >/dev/null 2>&1; then
  holder="$(lsof -nP -iTCP:3030 -sTCP:LISTEN -t 2>/dev/null || true)"
  holder="$(printf '%s\n' "$holder" | head -n 1)"
  if [ -n "$holder" ]; then
    echo "spyder: stopping pid $holder still holding :3030"
    kill "$holder" 2>/dev/null || true
    i=0
    while [ "$i" -lt 20 ] && kill -0 "$holder" 2>/dev/null; do
      sleep 1
      i=$((i + 1))
    done
    kill -9 "$holder" 2>/dev/null || true
  fi
fi

supervisorctl reread
supervisorctl update
supervisorctl restart spyder 2>/dev/null || supervisorctl start spyder

echo "spyder installed at $DEST"
supervisorctl status spyder
