#!/bin/sh
# Install the spyder LaunchAgent: render launchd/com.marcelocantos.spyder.plist into
# ~/Library/LaunchAgents and (re)load it in the user's GUI domain. launchd
# runs the program directly, so macOS privacy prompts (TCC) name it rather
# than a supervisor. If an old supervisord program of the same name exists,
# this stops it and removes its config first. Exactly one parent may own
# :3030, so brew services and any stray listener are evicted too.
#
# The plist runs the Homebrew Cellar binary (day-to-day MCP). Tree-built
# spyder stays off :3030 — use :3131 for unreleased code. The empty
# SPYDER_RUNS_MAX_* values leave run retention at the daemon's defaults.
# Exit reasons and fatal panics are also appended to ~/.spyder/exits.log;
# spyder.log itself is rotated by the host, not by this agent.
#
#   LAUNCHD_SKIP_LOAD=1  render only; do not touch launchd or supervisord
#   SPYDER_IOS_AGENT_DEVICES=<udid,…|*>  renders the devicekit backend for
#                        those devices (🎯T175); unset renders alertrunner.
#                        UDIDs are per host, so they live only in the
#                        installed plist.
set -eu

NAME=spyder
LABEL=com.marcelocantos.spyder
REPO="$(CDPATH='' cd "$(dirname "$0")/.." && pwd)"
: "${HOME:?HOME is not set}"
AGENTS="$HOME/Library/LaunchAgents"
DEST="$AGENTS/$LABEL.plist"
DOMAIN="gui/$(id -u)"

# Resident iOS agent backend (🎯T175). Per-host UDIDs come from the
# installer's environment, never from the template.
IOS_AGENT_DEVICES="${SPYDER_IOS_AGENT_DEVICES:-}"
case "$IOS_AGENT_DEVICES" in
  *[!0-9A-Fa-f,*-]*) echo "SPYDER_IOS_AGENT_DEVICES must be UDIDs separated by commas, or *" >&2; exit 1 ;;
esac
if [ -n "$IOS_AGENT_DEVICES" ]; then IOS_AGENT_BACKEND=devicekit; else IOS_AGENT_BACKEND=alertrunner; fi

chmod +x "$REPO/launchd/run-spyder.sh" "$REPO/launchd/install.sh"

mkdir -p "$AGENTS" "$HOME/.local/var/log"
tmp="$(mktemp "$AGENTS/.$LABEL.XXXXXX")"
trap 'rm -f "$tmp"' EXIT HUP INT TERM
sed -e "s|@REPO@|$REPO|g" -e "s|@HOME@|$HOME|g" -e "s|@USER@|$(id -un)|g" \
  -e "s|@IOS_AGENT_BACKEND@|$IOS_AGENT_BACKEND|g" \
  -e "s|@IOS_AGENT_DEVICES@|$IOS_AGENT_DEVICES|g" \
  "$REPO/launchd/$LABEL.plist" > "$tmp"
plutil -lint "$tmp" >/dev/null
chmod 644 "$tmp"
mv "$tmp" "$DEST"
echo "rendered $DEST"

if [ "${LAUNCHD_SKIP_LOAD:-}" = 1 ]; then
  exit 0
fi

old=/opt/homebrew/etc/supervisor.d/$NAME.ini
if [ -f "$old" ]; then
  if command -v supervisorctl >/dev/null 2>&1; then
    supervisorctl stop "$NAME" || true
  fi
  rm -f "$old"
  if command -v supervisorctl >/dev/null 2>&1; then
    supervisorctl update >/dev/null || true
  fi
  echo "removed supervisord program $NAME"
fi

# The Homebrew service would compete for :3030.
if command -v brew >/dev/null 2>&1; then
  brew services stop spyder >/dev/null 2>&1 || true
fi
for brew_label in sh.brew.spyder homebrew.mxcl.spyder; do
  launchctl bootout "$DOMAIN/$brew_label" 2>/dev/null || true
done

launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true

# Anything still holding the port has no KeepAlive parent left to revive it.
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

# bootstrap can briefly fail while the old instance is still being torn down.
n=0
until launchctl bootstrap "$DOMAIN" "$DEST" 2>/dev/null; do
  n=$((n + 1))
  if [ "$n" -ge 10 ]; then
    launchctl bootstrap "$DOMAIN" "$DEST"
  fi
  sleep 1
done
launchctl print "$DOMAIN/$LABEL" | grep -E '^	(state|pid) = '
