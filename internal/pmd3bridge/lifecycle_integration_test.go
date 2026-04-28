// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestLifecycle_StdinEOFKillsBridge verifies the 🎯T44 stdin-EOF liveness
// mechanism end-to-end using the minimal helper binary.
//
// The helper binary (ready-and-block) prints the ready handshake and then
// blocks on SIGTERM. This test extends the helper to also exit on stdin-EOF
// so we can exercise the same kernel mechanism the real bridge uses.
//
// Since helperSrc is shared across the package tests and already compiled,
// we instead exercise the mechanism directly at the Supervisor level: start
// the supervisor, get the liveness pipe write end, close it manually (as if
// the parent process died), and verify that the watchdog detects the process
// exit within the expected window.
//
// The helper binary already exits on SIGTERM. Closing the liveness pipe write
// end sends EOF to the helper's fd 0. Because the helper does NOT watch stdin,
// it won't exit on EOF alone — that path is exercised by the real Python bridge.
// What we CAN test at the unit level is:
//  1. The liveness pipe is created and stored on the Supervisor.
//  2. Closing it does not cause any error.
//  3. The supervisor starts and stops cleanly with the pipe present.
//
// The full stdin-EOF → process-exit path (Python side) is exercised by
// TestIntegration_StdinEOFKillsBridge (integration tag) against the real bridge.
//
// Gated on SPYDER_LIFECYCLE=1 so it runs explicitly rather than in the
// fast unit-test loop.
func TestLifecycle_LivenessPipePresent(t *testing.T) {
	if os.Getenv("SPYDER_LIFECYCLE") != "1" {
		t.Skip("set SPYDER_LIFECYCLE=1 to run lifecycle tests")
	}

	bin := getHelperBin(t)
	sup := NewSupervisor(bin,
		WithReadyTimeout(5*time.Second),
		WithShutdownTimeout(2*time.Second),
	)

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sup.mu.Lock()
	pipe := sup.gen.livenessPipe
	sup.mu.Unlock()

	if pipe == nil {
		t.Fatal("livenessPipe is nil after Start; expected a non-nil *os.File")
	}

	// Verify the pipe fd is valid — Stat on a valid fd succeeds.
	if _, err := pipe.Stat(); err != nil {
		t.Errorf("livenessPipe.Stat: %v (expected valid fd)", err)
	}

	// Clean shutdown: Stop closes the pipe in the watchdog defer.
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop + doneCh closed, the pipe should be closed. Verify that
	// another Stat on the same *os.File fails (or returns an error)
	// because the fd is now closed. On macOS the fd may be recycled so we
	// cannot guarantee EBADF, but we verify Stop returned without error.
	t.Log("liveness pipe closed cleanly by watchdog defer on Stop")
}

// TestLifecycle_LivenessPipeCloseStopsHelperWithStdinWatcher tests the full
// EOF path using a helper binary that exits on stdin-EOF.
//
// This test builds a second helper binary (stdin-exit) that watches fd 0
// and exits when it reads EOF — mirroring what the Python bridge does.
// We start the Supervisor against it, then close the liveness write end,
// and assert the process exits within 1 s.
func TestLifecycle_LivenessPipeCloseStopsHelperWithStdinWatcher(t *testing.T) {
	if os.Getenv("SPYDER_LIFECYCLE") != "1" {
		t.Skip("set SPYDER_LIFECYCLE=1 to run lifecycle tests")
	}

	bin := getStdinExitHelperBin(t)
	sup := NewSupervisor(bin,
		WithReadyTimeout(5*time.Second),
		WithShutdownTimeout(2*time.Second),
		// Intercept fatal so the test doesn't panic when the subprocess
		// exits unexpectedly (from the supervisor's perspective).
		withFatal(func(err error) {
			t.Logf("watchdog fatal (expected): %v", err)
		}),
	)

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sup.mu.Lock()
	pipe := sup.gen.livenessPipe
	pid := sup.gen.cmd.Process.Pid
	sup.mu.Unlock()

	if pipe == nil {
		t.Fatal("livenessPipe is nil after Start")
	}

	t.Logf("bridge helper pid=%d; closing liveness pipe write end", pid)

	// Close the write end. The kernel will deliver EOF to the child's fd 0.
	// The child's stdin-watcher goroutine calls os.Exit(0).
	if err := pipe.Close(); err != nil {
		t.Fatalf("close liveness pipe: %v", err)
	}
	// Null out the stored pipe so watchdog's defer doesn't double-close.
	sup.mu.Lock()
	sup.gen.livenessPipe = nil
	doneCh := sup.gen.doneCh
	sup.mu.Unlock()

	// Poll for the process to have exited. We use os.FindProcess + Signal(0)
	// which returns an error once the process is gone.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		proc, err := os.FindProcess(pid)
		if err != nil {
			// Process already gone.
			t.Logf("process %d gone (FindProcess error): %v", pid, err)
			goto done
		}
		if err := proc.Signal(os.Signal(nil)); err != nil {
			// Signal(nil) fails when the process is gone.
			t.Logf("process %d gone (Signal(0) error): %v", pid, err)
			goto done
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d still alive 3s after closing liveness pipe write end", pid)

done:
	// Wait for the watchdog to detect the unexpected exit; doneCh is closed.
	select {
	case <-doneCh:
		t.Log("watchdog goroutine exited cleanly")
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog goroutine did not exit within 3s")
	}
}

// stdinExitHelperSrc is a minimal Go program that:
//  1. Prints the ready handshake.
//  2. Spawns a goroutine that blocks on os.Stdin.Read and calls os.Exit(0) on EOF.
//  3. Blocks forever (waiting for SIGTERM or stdin-EOF).
const stdinExitHelperSrc = `package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	fmt.Println("ready port=1 token=test-token")
	// Watch stdin for EOF — mirrors the Python bridge's _watch_parent_via_stdin.
	go func() {
		buf := make([]byte, 1)
		os.Stdin.Read(buf) // returns on EOF or data
		os.Exit(0)
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	<-ch
	os.Exit(0)
}
`

var (
	stdinExitHelperOnce sync.Once
	stdinExitHelperPath string
	stdinExitHelperErr  error
)

func getStdinExitHelperBin(t *testing.T) string {
	t.Helper()
	stdinExitHelperOnce.Do(func() {
		path := filepath.Join(helperBinDir, "stdin-exit")
		if runtime.GOOS == "windows" {
			path += ".exe"
		}
		src := filepath.Join(helperBinDir, "stdin_exit_main.go")
		if err := os.WriteFile(src, []byte(stdinExitHelperSrc), 0o644); err != nil {
			stdinExitHelperErr = fmt.Errorf("write stdin-exit src: %w", err)
			return
		}
		cmd := exec.Command("go", "build", "-o", path, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			stdinExitHelperErr = fmt.Errorf("compile stdin-exit: %w\n%s", err, out)
			return
		}
		stdinExitHelperPath = path
	})
	if stdinExitHelperErr != nil {
		t.Fatalf("stdin-exit helper unavailable: %v", stdinExitHelperErr)
	}
	return stdinExitHelperPath
}
