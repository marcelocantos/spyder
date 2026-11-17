// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/memguard"
	"github.com/marcelocantos/spyder/internal/paths"
)

const memguardChildEnv = "SPYDER_TEST_MEMGUARD_CHILD"

// A runaway allocation in the daemon trips the guard, which writes a
// goroutine dump and heap profile and exits 1 for launchd to restart
// (🎯T195). The child mimics the 15 Nov loop: append without bound.
func TestMemguardExit_RunawayExitsWithEvidence(t *testing.T) {
	if os.Getenv(memguardChildEnv) == "1" {
		go memguard.Run(context.Background(), memguard.Args{
			Limit: 256 << 20, Interval: 10 * time.Millisecond, OnExceed: memguardExit,
		})
		var runaway [][]byte
		for {
			runaway = append(runaway, make([]byte, 1<<20))
			time.Sleep(time.Millisecond)
		}
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMemguardExit_RunawayExitsWithEvidence$")
	cmd.Env = append(os.Environ(), memguardChildEnv+"=1", paths.DataDirEnv+"="+dir)
	start := time.Now()
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("child exit: %v", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("guard took %s", took)
	}
	for _, pattern := range []string{"memguard-*-goroutine.txt", "memguard-*-heap.pprof"} {
		got, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(got) != 1 {
			t.Fatalf("%s: %v", pattern, got)
		}
	}
	dump, _ := filepath.Glob(filepath.Join(dir, "memguard-*-goroutine.txt"))
	if b, _ := os.ReadFile(dump[0]); !strings.Contains(string(b), "TestMemguardExit_RunawayExitsWithEvidence") {
		t.Fatal("goroutine dump does not show the allocating goroutine")
	}
}
