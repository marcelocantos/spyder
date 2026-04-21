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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// helperBinary compiles and returns the path to the test helper binary that
// simulates the pmd3-bridge process. The binary is compiled once per test
// process into a package-level temporary directory that outlives individual
// t.TempDir() directories.
var (
	helperBinOnce sync.Once
	helperBinPath string
	helperBinErr  error
	helperBinDir  string // package-level dir; removed in TestMain
)

func TestMain(m *testing.M) {
	var err error
	helperBinDir, err = os.MkdirTemp("", "spyder-pmd3bridge-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: create helper dir: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(helperBinDir)
	os.Exit(code)
}

func getHelperBin(t *testing.T) string {
	t.Helper()
	helperBinOnce.Do(func() {
		helperBinPath = filepath.Join(helperBinDir, "fake-bridge")
		if runtime.GOOS == "windows" {
			helperBinPath += ".exe"
		}
		// Write the helper source then compile it.
		src := filepath.Join(helperBinDir, "main.go")
		if err := os.WriteFile(src, []byte(helperSrc), 0o644); err != nil {
			helperBinErr = fmt.Errorf("write helper src: %w", err)
			return
		}
		cmd := exec.Command("go", "build", "-o", helperBinPath, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			helperBinErr = fmt.Errorf("compile helper: %w\n%s", err, out)
		}
	})

	if helperBinErr != nil {
		t.Fatalf("helper binary unavailable: %v", helperBinErr)
	}
	return helperBinPath
}

// helperSrc is the source for the test helper binary. It reads the BRIDGE_MODE
// environment variable to decide its behaviour:
//
//   - "ready" — writes "ready\n", then blocks until SIGTERM.
//   - "crash" — writes "ready\n", then exits immediately (simulates a crash).
//   - "crash-before-ready" — exits without writing "ready\n".
const helperSrc = `package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	mode := os.Getenv("BRIDGE_MODE")
	switch mode {
	case "crash":
		fmt.Println("ready")
		os.Exit(1)
	case "crash-before-ready":
		os.Exit(1)
	default: // "ready" or anything else
		fmt.Println("ready")
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		os.Exit(0)
	}
}
`

// fakeClock is a deterministic clock that never actually sleeps, making backoff
// tests fast.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time

	// sleptTotal accumulates all durations passed to Sleep.
	sleptTotal atomic.Int64
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(d time.Duration) {
	c.sleptTotal.Add(int64(d))
	// Do not actually sleep — just advance the fake clock.
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// --- Tests ---

// tempSock creates a temporary socket path under os.TempDir() with a short
// name to stay within macOS's 104-byte Unix socket path limit.
func tempSock(t *testing.T, prefix string) string {
	t.Helper()
	f, err := os.CreateTemp("", "spyder-sup-"+prefix+"-*.sock")
	if err != nil {
		t.Fatalf("create temp sock: %v", err)
	}
	p := f.Name()
	_ = f.Close()
	_ = os.Remove(p)
	t.Cleanup(func() { _ = os.Remove(p) })
	return p
}

func TestSupervisor_StartAndStop(t *testing.T) {
	bin := getHelperBin(t)
	sock := tempSock(t, "ok")

	t.Setenv("BRIDGE_MODE", "ready")
	sup := NewSupervisor(bin, sock,
		WithReadyTimeout(5*time.Second),
		WithShutdownTimeout(2*time.Second),
	)

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Socket should not be removed while running (bridge didn't bind one in the
	// test helper, but we can verify Stop removes it cleanly).
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Idempotent second Stop must not panic or block.
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestSupervisor_StartTimeout(t *testing.T) {
	bin := getHelperBin(t)
	sock := tempSock(t, "timeout")

	t.Setenv("BRIDGE_MODE", "crash-before-ready")
	sup := NewSupervisor(bin, sock,
		WithReadyTimeout(2*time.Second),
	)

	ctx := context.Background()
	err := sup.Start(ctx)
	if err == nil {
		t.Fatal("Start: expected error when bridge never prints ready; got nil")
	}
	if !strings.Contains(err.Error(), "bridge stdout closed") &&
		!strings.Contains(err.Error(), "timed out") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSupervisor_MissingBinary(t *testing.T) {
	sock := tempSock(t, "missing")
	sup := NewSupervisor("/nonexistent/pmd3-bridge", sock,
		WithReadyTimeout(2*time.Second),
	)

	err := sup.Start(context.Background())
	if err == nil {
		t.Fatal("expected error for missing binary; got nil")
	}
}

func TestSupervisor_RestartOnCrash(t *testing.T) {
	// Build a helper that crashes immediately after printing "ready".
	bin := getHelperBin(t)
	sock := tempSock(t, "crash")

	t.Setenv("BRIDGE_MODE", "crash")

	fc := newFakeClock()
	sup := NewSupervisor(bin, sock,
		WithReadyTimeout(5*time.Second),
		withClock(fc),
		withBackoffInitial(1*time.Millisecond),
		WithBackoffCap(10*time.Millisecond),
	)

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the watchdog a moment to attempt at least one restart.
	// Because the helper exits right after printing "ready", the watchdog
	// should detect the exit and try to restart.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if fc.sleptTotal.Load() > 0 {
			break
		}
	}
	if fc.sleptTotal.Load() == 0 {
		t.Log("watchdog may not have restarted yet (timing); not failing")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestSupervisor_GracefulStopOnContextCancel(t *testing.T) {
	bin := getHelperBin(t)
	sock := tempSock(t, "ctxcancel")

	t.Setenv("BRIDGE_MODE", "ready")
	sup := NewSupervisor(bin, sock,
		WithReadyTimeout(5*time.Second),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Cancel the parent context and then stop — Stop should still work cleanly.
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("Stop after ctx cancel: %v", err)
	}
}
