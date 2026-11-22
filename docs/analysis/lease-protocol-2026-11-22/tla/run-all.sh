#!/bin/bash
# Check every configuration of Leases.tla; outputs go to ../out/tlc-<cfg>.out
# and condensed counterexamples to ../out/trace-<cfg>.txt.
set -eu
cd "$(dirname "$0")"
for cfg in base base_agent big_nofault live_old_new live_old_new_end live_new_agent live_new_agent_end \
           orphan sleep flap flap_agent old_erase old_name daemon_down \
           fix_flap fix_flap_agent fix_live_new_agent; do
  echo "### $cfg"
  ./run-tlc.sh "$cfg" | grep -E "No error|violated|distinct states found, 0" || true
  if grep -q "is violated" "../out/tlc-$cfg.out"; then
    python3 -I trace.py "../out/tlc-$cfg.out" > "../out/trace-$cfg.txt"
  fi
done
