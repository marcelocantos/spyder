#!/bin/bash
# R2: SIGKILL to the process group of a non-interactive `spyder run`
# (what `pueue kill` and a launchd job stop deliver) kills the wrapper but
# not the wrapped job, which the T208 fix put in a group of its own. The
# lease then lapses while the job still runs, and a second job is granted.
# Usage: r2-group-kill-orphans-job.sh <spyder-binary>
set -eu
BIN=$1
D=$(mktemp -d)
export SPYDER_DATA_DIR="$D/data" HOME="$D/home"
mkdir -p "$HOME"
X=00008103-00000000000000AA
book="$SPYDER_DATA_DIR/reservations.json"
# Start the wrapper as a process-group leader, as pueue starts a task.
perl -e 'setpgrp(0,0); exec @ARGV' "$BIN" run --device "$X" --as job-A -- \
  sh -c "echo \$\$ > '$D/job.pid'; while :; do sleep 0.2; done" </dev/null 2>"$D/a.err" &
W=$!
i=0; while [ ! -s "$D/job.pid" ]; do i=$((i+1)); [ $i -lt 100 ] || exit 1; sleep 0.1; done
J=$(cat "$D/job.pid")
echo "wrapper pid $W pgid $(ps -o pgid= -p $W | tr -d ' '); job pid $J pgid $(ps -o pgid= -p $J | tr -d ' ')"
kill -KILL -- "-$W"
sleep 0.5
echo "after SIGKILL to the wrapper's group: wrapper alive=$(kill -0 $W 2>/dev/null && echo yes || echo no) job alive=$(kill -0 $J 2>/dev/null && echo yes || echo no)"
echo "== book (nothing renews it now)"; cat "$book"
# Stand in for the hour passing: rewrite the expiry into the past.
python3 -I -c 'import json,sys; p=sys.argv[1]; b=json.load(open(p)); b[0]["expires_at"]="2000-01-01T00:00:00Z"; json.dump(b,open(p,"w"))' "$book"
set +e
"$BIN" run --device "$X" --as job-B -- sh -c 'echo "job B granted the device"' </dev/null 2>/dev/null
echo "job B exit $?; job A's worker alive=$(kill -0 $J 2>/dev/null && echo yes || echo no)"
set -e
kill -KILL "$J" 2>/dev/null || true
rm -rf "$D"
