#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/source" "$fixture/spyder"
git -C "$fixture/source" init -q
git -C "$fixture/source" -c user.name=test -c user.email=test@example.org commit -qm seed --allow-empty
git -C "$fixture/spyder" init -q
cat > "$fixture/spyder/.gitmodules" <<'MODULES'
[submodule "player/vendor/dep"]
    path = player/vendor/dep
    url = https://example.invalid/dep
MODULES
store="$fixture/spyder/.git/modules/player/vendor/dep"
mkdir -p "$store/objects/info"
git init -q --bare "$store"
printf '%s/objects\n' "$fixture/source/.git" > "$store/objects/info/alternates"
git --git-dir="$store" update-ref refs/heads/main "$(git -C "$fixture/source" rev-parse HEAD)"
cp "$root/scripts/check-player-submodules.sh" "$fixture/spyder/check-player-submodules.sh"
if "$fixture/spyder/check-player-submodules.sh" >/dev/null 2>&1; then
    echo 'audit accepted borrowed object store' >&2
    exit 1
fi
# Run the real script in the fixture: it locates the repo via its own path.
"$fixture/spyder/check-player-submodules.sh" --repair >/dev/null
[[ ! -e "$store/objects/info/alternates" ]]
git --git-dir="$store" fsck --full --no-dangling
"$fixture/spyder/check-player-submodules.sh" >/dev/null
echo 'check-player-submodules fixture PASS'
