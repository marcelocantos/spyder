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
// environment variable to decide its behaviour. All "ready" variants emit the
// same structured ready line as the real bridge: `ready port=NNNN token=XXXX`.
//
//   - "ready" — writes structured ready line, then blocks until SIGTERM.
//   - "crash" — writes the ready line, then exits immediately (simulates
//     an unexpected exit after a successful handshake).
//   - "crash-before-ready" — exits without writing anything.
//   - "malformed-ready" — writes a ready line missing port/token.
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
		fmt.Println("ready port=1 token=test-token")
		os.Exit(1)
	case "crash-before-ready":
		os.Exit(1)
	case "malformed-ready":
		fmt.Println("ready")
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		os.Exit(0)
	default: // "ready" or anything else
		fmt.Println("ready port=1 token=test-token")
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		os.Exit(0)
	}
}
`

// --- Tests ---

func TestSupervisor_StartAndStop(t *testing.T) {
	bin := getHelperBin(t)

	t.Setenv("BRIDGE_MODE", "ready")
	sup := NewSupervisor(bin,
		WithReadyTimeout(5*time.Second),
		WithShutdownTimeout(2*time.Second),
	)

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sup.Token() != "test-token" {
		t.Errorf("Token = %q; want test-token", sup.Token())
	}
	if sup.BaseURL() != "http://127.0.0.1:1" {
		t.Errorf("BaseURL = %q; want http://127.0.0.1:1", sup.BaseURL())
	}

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

	t.Setenv("BRIDGE_MODE", "crash-before-ready")
	sup := NewSupervisor(bin,
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

func TestSupervisor_MalformedReady(t *testing.T) {
	bin := getHelperBin(t)
	t.Setenv("BRIDGE_MODE", "malformed-ready")
	sup := NewSupervisor(bin, WithReadyTimeout(2*time.Second))

	err := sup.Start(context.Background())
	if err == nil {
		t.Fatal("Start: expected malformed-ready error; got nil")
	}
	if !strings.Contains(err.Error(), "malformed ready line") &&
		!strings.Contains(err.Error(), "missing") {
		t.Errorf("unexpected error: %v", err)
	}
	// Clean up the hanging subprocess the helper spawned.
	_ = sup.Stop(context.Background())
}

func TestSupervisor_MissingBinary(t *testing.T) {
	sup := NewSupervisor("/nonexistent/pmd3-bridge",
		WithReadyTimeout(2*time.Second),
	)

	err := sup.Start(context.Background())
	if err == nil {
		t.Fatal("expected error for missing binary; got nil")
	}
}

// TestSupervisor_PanicsOnUnexpectedExit asserts that when the subprocess
// exits before Stop is called, the supervisor invokes its fatal hook rather
// than silently restarting. This is the core 🎯T26.2 behaviour: bridge
// death is a bug, not a recoverable condition.
func TestSupervisor_PanicsOnUnexpectedExit(t *testing.T) {
	bin := getHelperBin(t)

	t.Setenv("BRIDGE_MODE", "crash")

	fatalCh := make(chan error, 1)
	sup := NewSupervisor(bin,
		WithReadyTimeout(5*time.Second),
		withFatal(func(err error) { fatalCh <- err }),
	)

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case err := <-fatalCh:
		if err == nil {
			t.Fatal("fatal called with nil error")
		}
		if !strings.Contains(err.Error(), "subprocess exited unexpectedly") {
			t.Errorf("unexpected fatal message: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fatal hook not called after subprocess exit")
	}
}

func TestSupervisor_GracefulStopOnContextCancel(t *testing.T) {
	bin := getHelperBin(t)

	t.Setenv("BRIDGE_MODE", "ready")
	sup := NewSupervisor(bin,
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
