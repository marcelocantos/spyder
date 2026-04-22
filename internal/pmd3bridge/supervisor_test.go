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

// The supervisor tests here cover lifecycle primitives — Start / Stop /
// watchdog's fatal-on-exit path / ready-handshake timeout. They do NOT
// simulate a misbehaving bridge (🎯T26.4): the bridge is paired with the
// daemon and not treated as a hostile external service. Scenarios that
// require a subprocess use either the minimal protocol-conformant helper
// below (prints a valid ready line, blocks on SIGTERM) or plumbing-level
// commands (`sh -c '…'`, `/bin/sleep`) that aren't pretending to be a
// bridge — they're just processes with specific exit / output behaviours.

var (
	helperBinOnce sync.Once
	helperBinPath string
	helperBinErr  error
	helperBinDir  string
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

// getHelperBin returns the path to a minimal protocol-conformant helper.
// The helper prints a valid `ready port=1 token=test-token` line and
// blocks on SIGTERM. It exists so lifecycle tests (Start / Stop /
// idempotency / graceful shutdown) can exercise the supervisor without
// spawning the real Python bridge — the supervisor only needs a process
// that conforms to the handshake and responds to SIGTERM. The helper is
// NOT a simulated bridge; it has no per-test behaviour switches and
// never fakes failure.
func getHelperBin(t *testing.T) string {
	t.Helper()
	helperBinOnce.Do(func() {
		helperBinPath = filepath.Join(helperBinDir, "ready-and-block")
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

const helperSrc = `package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	fmt.Println("ready port=1 token=test-token")
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	<-ch
	os.Exit(0)
}
`

// --- Lifecycle happy-path ---------------------------------------------------

func TestSupervisor_StartAndStop(t *testing.T) {
	bin := getHelperBin(t)
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

func TestSupervisor_GracefulStopOnContextCancel(t *testing.T) {
	bin := getHelperBin(t)
	sup := NewSupervisor(bin,
		WithReadyTimeout(5*time.Second),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("Stop after ctx cancel: %v", err)
	}
}

// --- Detection primitives ---------------------------------------------------

// TestSupervisor_ReadyTimeout asserts that a subprocess that never prints
// a `ready` line is detected by the ready-handshake timeout. We use
// `/bin/sleep` — a real process that happens to be silent — rather than a
// fake bridge configured to withhold the ready line.
func TestSupervisor_ReadyTimeout(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not on PATH")
	}
	sup := newSupervisorForCmd(t, sleep, []string{"30"},
		WithReadyTimeout(200*time.Millisecond))

	if err := sup.Start(context.Background()); err == nil {
		t.Fatal("Start: expected timeout; got nil")
	} else if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSupervisor_StdoutClosedBeforeReady asserts that a subprocess whose
// stdout closes before emitting `ready` is detected. `/bin/true` exits
// immediately with closed stdout.
func TestSupervisor_StdoutClosedBeforeReady(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true not on PATH")
	}
	sup := newSupervisorForCmd(t, trueBin, nil,
		WithReadyTimeout(2*time.Second))

	if err := sup.Start(context.Background()); err == nil {
		t.Fatal("Start: expected stdout-closed error; got nil")
	} else if !strings.Contains(err.Error(), "bridge stdout closed") &&
		!strings.Contains(err.Error(), "timed out") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSupervisor_PanicsOnUnexpectedExit asserts that when a successfully-
// handshaked subprocess exits before Stop is called, the supervisor
// invokes its fatal hook. The subprocess is a one-liner shell script:
// echo the ready line, then exit 1. It is not pretending to be a broken
// bridge — it is a minimal process that exercises "watched subprocess
// exits; watchdog fires fatal".
func TestSupervisor_PanicsOnUnexpectedExit(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	script := `echo "ready port=1 token=test-token"; exit 1`
	sup := newSupervisorForCmd(t, sh, []string{"-c", script},
		WithReadyTimeout(5*time.Second))

	fatalCh := make(chan error, 1)
	withFatal(func(err error) { fatalCh <- err })(sup)

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case err := <-fatalCh:
		if !strings.Contains(err.Error(), "subprocess exited unexpectedly") {
			t.Errorf("unexpected fatal message: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fatal hook not called after subprocess exit")
	}
}

func TestSupervisor_MissingBinary(t *testing.T) {
	sup := NewSupervisor("/nonexistent/pmd3-bridge",
		WithReadyTimeout(2*time.Second),
	)
	if err := sup.Start(context.Background()); err == nil {
		t.Fatal("expected error for missing binary; got nil")
	}
}

// newSupervisorForCmd builds a Supervisor around an arbitrary command +
// args (for cases where the minimal helper binary is the wrong shape).
// Only for tests.
func newSupervisorForCmd(t *testing.T, bin string, args []string, opts ...Option) *Supervisor {
	t.Helper()
	// The production Supervisor spawns binaryPath with no args and reads
	// the structured ready line. For tests that need a specific argv, we
	// shell out via sh -c. This function packages that pattern as a helper
	// so the Go `NewSupervisor(binaryPath)` API stays zero-arg.
	if len(args) == 0 {
		return NewSupervisor(bin, opts...)
	}
	// When we need args, pipe through sh -c joined.
	joined := bin
	for _, a := range args {
		joined += " " + shellQuote(a)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	// Create a wrapper script on disk: argv is ["sh", "-c", script].
	wrapperDir := t.TempDir()
	script := filepath.Join(wrapperDir, "wrapper.sh")
	content := "#!/bin/sh\nexec " + joined + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	_ = sh // ensure sh is reachable; we invoke script directly via shebang
	return NewSupervisor(script, opts...)
}

func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t\n\"'\\$`") {
		return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	return s
}
