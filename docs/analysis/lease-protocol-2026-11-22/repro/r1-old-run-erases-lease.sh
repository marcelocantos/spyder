#!/bin/bash
# R1: an old `spyder run` (master) erases a lease the new code granted.
# Usage: r1-old-run-erases-lease.sh <spyder-old> <spyder-new>
# Runs entirely in a temporary SPYDER_DATA_DIR and HOME. The device refs are
# iOS-UDID-shaped, so neither binary lists connected devices.
set -eu
OLD=$1 NEW=$2
D=$(mktemp -d)
export SPYDER_DATA_DIR="$D/data" HOME="$D/home"
mkdir -p "$HOME"
X=00008103-00000000000000AA
Y=00008103-00000000000000BB
book="$SPYDER_DATA_DIR/reservations.json"
wait_for() { i=0; while ! grep -q "$1" "$book" 2>/dev/null; do i=$((i+1)); [ $i -lt 100 ] || { echo "timeout waiting for $1"; exit 1; }; sleep 0.1; done; }

"$OLD" run --device "$X" --as job-A-old -- sh -c "while [ ! -e '$D/stopA' ]; do sleep 0.1; done" </dev/null 2>"$D/a.err" &
A=$!
wait_for job-A-old
"$NEW" run --device "$Y" --as job-B-new -- sh -c "while [ ! -e '$D/stopB' ]; do sleep 0.1; done" </dev/null 2>"$D/b.err" &
B=$!
wait_for job-B-new
echo "== book while A (old) holds X and B (new) holds Y"; cat "$book"
touch "$D/stopA"; wait $A || true
echo "== book after A exits; B is still running: $(kill -0 $B 2>/dev/null && echo yes || echo no)"; cat "$book"
set +e
"$NEW" run --device "$Y" --as job-C-new -- sh -c 'echo "job C is running on Y"' </dev/null 2>"$D/c.err"
echo "job C exit status: $?"
set -e
echo "B still running: $(kill -0 $B 2>/dev/null && echo yes || echo no)"
touch "$D/stopB"; wait $B || true
echo "== stderr of B"; cat "$D/b.err"
rm -rf "$D"
