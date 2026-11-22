#!/bin/bash
# R8: many short master `spyder run` processes at once, each on its own
# device, then report whether the book still parses and what it holds.
# Every run has released when this checks, so any entry left is a lease
# that a stale copy wrote back. (BSD xargs gives each run /dev/null as stdin.) Usage: r8-old-writers-race.sh <spyder-old> [runs] [parallel]
set -eu
OLD=$1 N=${2:-200} P=${3:-16}
D=$(mktemp -d)
export SPYDER_DATA_DIR="$D/data" HOME="$D/home"
mkdir -p "$HOME"
seq -f '00008103-%016g' 1 "$N" | xargs -P "$P" -I{} "$OLD" run --device {} --as job-{} -- true >/dev/null 2>"$D/err" || true
book="$SPYDER_DATA_DIR/reservations.json"
python3 -I -c '
import json, sys
raw = open(sys.argv[1]).read()
try:
    b = json.loads(raw)
except Exception as e:
    print("book does not parse:", e); print(raw[-200:]); sys.exit(0)
print("book parses; live entries left after every run released:", len(b))
' "$book"
grep -c "reservations unavailable" "$D/err" | sed 's/^/runs refused at start (unparseable book): /'
rm -rf "$D"
