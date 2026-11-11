// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"slices"
	"strings"
	"testing"
)

func TestOrphanedLogTails(t *testing.T) {
	cmd := strings.Join(logTailArgv[1:], " ")
	ps := strings.Join([]string{
		"  101     1 /usr/bin/log " + cmd,                   // orphan: reap
		"  102  5138 /usr/bin/log " + cmd,                   // live daemon's tail: keep
		"  103     1 log " + cmd,                            // orphan without a path: reap
		"  104     1 /usr/bin/log stream --process usbmuxd", // someone else's tail: keep
		"  105     1 /opt/homebrew/bin/spyder serve",        // not a log-tail: keep
		"garbage",
		"",
	}, "\n")
	got := orphanedLogTails(ps)
	if want := []int{101, 103}; !slices.Equal(got, want) {
		t.Fatalf("orphanedLogTails = %v, want %v", got, want)
	}
}

func TestReapOrphanedLogTailsKillsOnlyOrphans(t *testing.T) {
	cmd := strings.Join(logTailArgv[1:], " ")
	saveList, saveKill := listProcessesFn, killProcessFn
	t.Cleanup(func() { listProcessesFn, killProcessFn = saveList, saveKill })
	listProcessesFn = func() ([]byte, error) {
		return []byte("201 1 /usr/bin/log " + cmd + "\n202 999 /usr/bin/log " + cmd + "\n"), nil
	}
	var killed []int
	killProcessFn = func(pid int) error { killed = append(killed, pid); return nil }
	reapOrphanedLogTails()
	if !slices.Equal(killed, []int{201}) {
		t.Fatalf("killed %v, want [201]", killed)
	}
}
