#!/bin/bash
# Run TLC on one configuration of Leases.tla: run-tlc.sh <cfg-name> [outdir]
# Uses ~/.local/lib/tla2tools.jar; two workers, metadata in a temp dir.
set -eu
cd "$(dirname "$0")"
name=$1 out=${2:-../out}
meta=$(mktemp -d)
java -XX:+UseParallelGC -Xmx4g -cp "$HOME/.local/lib/tla2tools.jar" tlc2.TLC \
  -workers 2 -deadlock -metadir "$meta" -config "$name.cfg" MC.tla > "$out/tlc-$name.out" 2>&1 || true
rm -rf "$meta"
grep -E "Model checking completed|Error:|violated|distinct states|Finished in" "$out/tlc-$name.out" || true
