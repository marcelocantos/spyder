#!/bin/sh
sleep 900 &
echo $! > /Volumes/Storage/worktrees/spyder-lease-analysis-2026-11-22/pkill/bg.pid
echo $$ > /Volumes/Storage/worktrees/spyder-lease-analysis-2026-11-22/pkill/sh.pid
wait
