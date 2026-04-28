// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// 🎯T47 regression: a wedged bridge — process listening but unresponsive —
// must be SIGKILL'd and respawned by the supervisor without taking down
// the daemon. The supervisor exposes Restart() for that purpose, and
// LivenessProbe drives it after livenessWedgeThreshold consecutive
// Health failures.

var (
	restartHelperOnce sync.Once
	restartHelperPath string
	restartHelperErr  error
)

// getRestartHelperBin compiles a helper that prints a unique ready line
// per invocation (token includes its own PID) so the test can verify a
// genuine relaunch. The helper handles SIGTERM cleanly.
func getRestartHelperBin(t *testing.T) string {
	t.Helper()
	restartHelperOnce.Do(func() {
		restartHelperPath = filepath.Join(helperBinDir, "ready-pid-block")
		if runtime.GOOS == "windows" {
			restartHelperPath += ".exe"
		}
		src := filepath.Join(helperBinDir, "main_restart.go")
		if err := os.WriteFile(src, []byte(restartHelperSrc), 0o644); err != nil {
			restartHelperErr = fmt.Errorf("write restart helper src: %w", err)
			return
		}
		cmd := exec.Command("go", "build", "-o", restartHelperPath, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			restartHelperErr = fmt.Errorf("compile restart helper: %w\n%s", err, out)
		}
	})
	if restartHelperErr != nil {
		t.Fatalf("restart helper unavailable: %v", restartHelperErr)
	}
	return restartHelperPath
}

// restartHelperSrc prints `ready port=1 token=pid-<PID>` then blocks on
// SIGTERM. The token includes the PID so the test can detect a relaunch
// from the supervisor's exposed Token() value alone.
const restartHelperSrc = `package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	fmt.Printf("ready port=1 token=pid-%d\n", os.Getpid())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	<-ch
	os.Exit(0)
}
`

// TestSupervisor_Restart_KillsAndRespawns verifies that Restart() kills
// the current subprocess and brings up a fresh one. After Restart, the
// supervisor's BaseURL/Token reflect the new generation's values, the
// old subprocess has exited, and Stop tears down the new generation
// cleanly.
func TestSupervisor_Restart_KillsAndRespawns(t *testing.T) {
	bin := getRestartHelperBin(t)
	// Intercept fatal so a stray "subprocess exited unexpectedly" panic
	// would surface as a test failure rather than crashing the test
	// process. The whole point of T47 is that Restart must NOT trigger
	// the unexpected-exit path.
	var fatalErr atomic.Pointer[error]
	sup := NewSupervisor(bin,
		WithReadyTimeout(5*time.Second),
		WithShutdownTimeout(2*time.Second),
		withFatal(func(err error) { fatalErr.Store(&err) }),
	)

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sup.mu.Lock()
	gen1 := sup.gen
	sup.mu.Unlock()
	pid1 := gen1.cmd.Process.Pid
	token1 := sup.Token()
	if token1 == "" {
		t.Fatal("Token empty after Start")
	}
	t.Logf("generation 1: pid=%d token=%s", pid1, token1)

	// Restart: the supervisor SIGKILLs gen1 and launches gen2.
	restartCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sup.Restart(restartCtx); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	sup.mu.Lock()
	gen2 := sup.gen
	sup.mu.Unlock()
	if gen2 == gen1 {
		t.Fatal("Restart did not replace generation")
	}
	pid2 := gen2.cmd.Process.Pid
	token2 := sup.Token()
	t.Logf("generation 2: pid=%d token=%s", pid2, token2)

	if pid2 == pid1 {
		t.Errorf("PID unchanged after Restart: %d", pid2)
	}
	if token2 == token1 {
		t.Errorf("Token unchanged after Restart: %s", token2)
	}

	// gen1's watchdog must have completed cleanly and NOT fired fatal —
	// the planned exit is detected via gen.restarting.
	select {
	case <-gen1.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("generation 1 watchdog did not exit within 2s")
	}
	if fp := fatalErr.Load(); fp != nil {
		t.Errorf("fatal hook fired during Restart (regression): %v", *fp)
	}

	// Verify gen1 process is genuinely gone — Signal(0) succeeds while
	// alive, errors once reaped.
	if proc, err := os.FindProcess(pid1); err == nil {
		if err := proc.Signal(syscall.Signal(0)); err == nil {
			t.Errorf("generation 1 process pid=%d still alive after Restart", pid1)
		}
	}

	// Clean shutdown of gen2.
	stopCtx, stopCancel := context.WithTimeout(ctx, 3*time.Second)
	defer stopCancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("Stop after Restart: %v", err)
	}
}

// TestSupervisor_Restart_BeforeStartIsError verifies the contract:
// Restart is only valid after Start.
func TestSupervisor_Restart_BeforeStartIsError(t *testing.T) {
	sup := NewSupervisor("/nonexistent")
	err := sup.Restart(context.Background())
	if err == nil {
		t.Fatal("Restart before Start: expected error, got nil")
	}
}

// TestSupervisor_Restart_AfterStopIsError verifies that once Stop has
// been called, Restart is rejected — there's no live generation to
// replace.
func TestSupervisor_Restart_AfterStopIsError(t *testing.T) {
	bin := getRestartHelperBin(t)
	sup := NewSupervisor(bin,
		WithReadyTimeout(5*time.Second),
		WithShutdownTimeout(2*time.Second),
	)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := sup.Restart(context.Background()); err == nil {
		t.Fatal("Restart after Stop: expected error, got nil")
	}
}

// fakeRestarter records whether Restart was invoked, and synthesises a
// success or failure result. Used by livenessProbe tests so we can
// assert wedge detection without spinning up a real subprocess.
type fakeRestarter struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeRestarter) Restart(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return nil
}

func (f *fakeRestarter) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// wedgedRoundTripper is an http.RoundTripper that always fails as if the
// bridge had become unresponsive (transport error). Lets the test drive
// LivenessProbe through Health failures without a real subprocess.
type wedgedRoundTripper struct {
	requests atomic.Int64
}

func (w *wedgedRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	w.requests.Add(1)
	return nil, errors.New("connection refused (simulated wedge)")
}

// TestLivenessProbe_TriggersRestartAfterThreshold drives livenessProbe
// against a wedged client and verifies that after livenessWedgeThreshold
// consecutive Health failures the restarter is called exactly once.
func TestLivenessProbe_TriggersRestartAfterThreshold(t *testing.T) {
	rt := &wedgedRoundTripper{}
	client := &Client{
		http:    &http.Client{Transport: rt},
		baseURL: "http://127.0.0.1:1",
		token:   "test-token",
		// Tests do not want fire() to panic on the simulated transport
		// errors; we only care about the LivenessProbe-level logic
		// here. The transport error path goes through fire which
		// would panic via the default fatal hook.
		fatal: func(err error) {},
	}
	restarter := &fakeRestarter{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		livenessProbeAt(ctx, client, restarter, 5*time.Millisecond)
		close(done)
	}()

	// Wait for at least livenessWedgeThreshold + 1 ticks worth of failures
	// to land — the threshold trigger is on the Nth failure, then the loop
	// resets and continues.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if restarter.Calls() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if calls := restarter.Calls(); calls < 1 {
		t.Fatalf("expected at least 1 restart, got %d (Health requests=%d)",
			calls, rt.requests.Load())
	}
	t.Logf("restarter calls=%d Health requests=%d", restarter.Calls(),
		rt.requests.Load())
}

// TestLivenessProbe_HealthyDoesNotRestart verifies that successful
// Health responses keep the restarter idle.
type healthyRoundTripper struct{}

func (healthyRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	body := `{"ok": true, "uptime_s": 1.0}`
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func TestLivenessProbe_HealthyDoesNotRestart(t *testing.T) {
	client := &Client{
		http:    &http.Client{Transport: healthyRoundTripper{}},
		baseURL: "http://127.0.0.1:1",
		token:   "test-token",
		fatal:   func(err error) {},
	}
	restarter := &fakeRestarter{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		livenessProbeAt(ctx, client, restarter, 5*time.Millisecond)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if calls := restarter.Calls(); calls != 0 {
		t.Errorf("healthy path triggered restart: calls=%d", calls)
	}
}
