// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build device

package pmd3bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Resource-leak regression tests (🎯T27) that exercise the real pmd3
// and a real attached iOS device. They count open file descriptors
// (the axis that v0.7.0's services.list_devices was leaking on) before
// and after a fixed number of iterations, and assert the delta is
// bounded.
//
// Run with: SPYDER_DEVICES=1 go test -tags=device ./internal/pmd3bridge/...
// Requires a paired iOS device attached via USB.

func fdDevWrapper(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for range 8 {
		candidate := filepath.Join(dir, "scripts", "run-dev-bridge.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("scripts/run-dev-bridge.sh not found")
	return ""
}

// processGroupFDCount returns the total number of open file descriptors
// across every process in the given process group. Uses `lsof -g <pgid>`
// which reports one line per open file per process; we skip the header
// line and count the rest.
func processGroupFDCount(t *testing.T, pgid int) int {
	t.Helper()
	out, err := exec.Command("lsof", "-g", strconv.Itoa(pgid)).Output()
	if err != nil {
		// Exit 1 from lsof when it can't read some fds is fine — it still
		// prints the ones it could.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) == 0 {
			// treat as partial output
		} else {
			t.Fatalf("lsof -g %d: %v", pgid, err)
		}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) <= 1 {
		return 0
	}
	return len(lines) - 1 // subtract header
}

// TestDevice_ListDevices_NoFDLeak is the regression guard for the
// v0.7.0 fd leak (🎯T27). It spawns the real bridge against real pmd3
// and real devices, snapshots the bridge process-group fd count,
// calls ListDevices 50 times, snapshots again, and asserts the delta
// is below a generous threshold.
//
// Pre-fix behaviour (v0.7.0): every ListDevices call opens one lockdown
// connection per enumerated device and never closes it, leaking
// ~N_devices fds per call. 50 calls × 2 devices ≈ 100 fds — far above
// the threshold. Test FAILS.
//
// Post-fix behaviour: lockdown is scoped via _lockdown_ctx; fds return
// to baseline after each call. Delta ≈ 0. Test PASSES.
func TestDevice_ListDevices_NoFDLeak(t *testing.T) {
	script := fdDevWrapper(t)
	t.Setenv("SPYDER_LOG_LEVEL", "WARNING")

	sup := NewSupervisor(script,
		WithReadyTimeout(45*time.Second),
		WithShutdownTimeout(5*time.Second),
	)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("bridge Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = sup.Stop(stopCtx)
	})

	client := sup.Client()
	devs, err := client.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("initial ListDevices: %v", err)
	}
	if len(devs) == 0 {
		t.Skip("no iOS device attached; test requires at least one")
	}

	// Warm the bridge: the first few calls may allocate some caches that
	// pmd3 then reuses. We care about the per-call delta, not first-call
	// behaviour. Discard the first 5 calls' worth of fd churn.
	for range 5 {
		if _, err := client.ListDevices(context.Background()); err != nil {
			t.Fatalf("warm-up ListDevices: %v", err)
		}
	}
	time.Sleep(200 * time.Millisecond) // let any deferred closes settle

	pgid := sup.cmd.Process.Pid
	baseline := processGroupFDCount(t, pgid)
	t.Logf("baseline fd count (process group %d): %d", pgid, baseline)

	const iterations = 50
	for i := range iterations {
		if _, err := client.ListDevices(context.Background()); err != nil {
			t.Fatalf("ListDevices iteration %d: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	final := processGroupFDCount(t, pgid)
	delta := final - baseline
	t.Logf("after %d ListDevices calls: fd count = %d (delta = %+d)",
		iterations, final, delta)

	// Allow a generous buffer for unrelated churn (GC timing, Uvicorn
	// connection pool, etc.). Anything above 10 is a real leak.
	const maxDelta = 10
	if delta > maxDelta {
		t.Fatalf("fd count grew by %d over %d ListDevices calls (threshold %d) — resource leak",
			delta, iterations, maxDelta)
	}
}

// TestDevice_PowerAssertionLifecycle_NoFDLeak exercises the
// acquire→refresh→release cycle against a real device and asserts the
// bridge's fd count returns to baseline after release.
func TestDevice_PowerAssertionLifecycle_NoFDLeak(t *testing.T) {
	script := fdDevWrapper(t)
	t.Setenv("SPYDER_LOG_LEVEL", "WARNING")

	sup := NewSupervisor(script,
		WithReadyTimeout(45*time.Second),
		WithShutdownTimeout(5*time.Second),
	)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("bridge Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = sup.Stop(stopCtx)
	})

	client := sup.Client()
	devs, err := client.ListDevices(context.Background())
	if err != nil || len(devs) == 0 {
		t.Skip("no iOS device attached")
	}
	var target DeviceInfo
	for _, d := range devs {
		if d.Name != "" && d.Name != "unknown" {
			target = d
			break
		}
	}
	if target.UDID == "" {
		t.Skip("no paired iOS device")
	}

	// Warm up.
	for range 3 {
		h, err := client.AcquirePowerAssertion(context.Background(),
			target.UDID, "PreventUserIdleSystemSleep", "fd-warmup", 60, "")
		if err != nil {
			t.Fatalf("warm AcquirePowerAssertion: %v", err)
		}
		if err := client.ReleasePowerAssertion(context.Background(), h); err != nil {
			t.Fatalf("warm ReleasePowerAssertion: %v", err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	pgid := sup.cmd.Process.Pid
	baseline := processGroupFDCount(t, pgid)
	t.Logf("baseline fd count: %d", baseline)

	const iterations = 15
	for i := range iterations {
		h, err := client.AcquirePowerAssertion(context.Background(),
			target.UDID, "PreventUserIdleSystemSleep", "fd-leak-test", 60, "")
		if err != nil {
			t.Fatalf("AcquirePowerAssertion iter %d: %v", i, err)
		}
		if err := client.RefreshPowerAssertion(context.Background(), h, 60); err != nil {
			t.Fatalf("RefreshPowerAssertion iter %d: %v", i, err)
		}
		if err := client.ReleasePowerAssertion(context.Background(), h); err != nil {
			t.Fatalf("ReleasePowerAssertion iter %d: %v", i, err)
		}
	}
	time.Sleep(300 * time.Millisecond)

	final := processGroupFDCount(t, pgid)
	delta := final - baseline
	t.Logf("after %d acquire→refresh→release cycles: fd count = %d (delta = %+d)",
		iterations, final, delta)

	const maxDelta = 10
	if delta > maxDelta {
		t.Fatalf("fd count grew by %d over %d assertion cycles (threshold %d) — resource leak",
			delta, iterations, maxDelta)
	}
}
