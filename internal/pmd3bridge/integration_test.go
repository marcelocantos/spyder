// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package pmd3bridge

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Integration tests (🎯T26.4) spawn the real `pmd3-bridge` Python
// subprocess via scripts/run-dev-bridge.sh (uv + python -m), with the
// pmd3-requiring services module swapped for an in-process fake via
// SPYDER_BRIDGE_FAKE_SERVICES=1. Every other layer — Supervisor,
// Client, HTTP, auth middleware, NDJSON/octet-stream framing — is the
// production code path. No happy-path mocks.
//
// Gated behind the `integration` build tag so developers can exclude
// them from the fast loop. `make test-integration` or `SPYDER_
// INTEGRATION=1 make test-report` runs them.

// findDevBridge walks up from this source file's directory looking for
// scripts/run-dev-bridge.sh. Go test's CWD is the package dir, but
// runtime.Caller is more robust against GOPATH / module layout
// quirks.
func findDevBridge(t *testing.T) string {
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
	t.Fatalf("scripts/run-dev-bridge.sh not found walking up from %s", thisFile)
	return ""
}

// startFakeServicesBridge spawns the real bridge with the fake-services
// shim installed. Returns the supervisor + a live Client. Cleanup via
// t.Cleanup.
func startFakeServicesBridge(t *testing.T) (*Supervisor, *Client) {
	t.Helper()
	script := findDevBridge(t)

	t.Setenv("SPYDER_BRIDGE_FAKE_SERVICES", "1")
	t.Setenv("SPYDER_LOG_LEVEL", "WARNING") // quiet for test output

	sup := NewSupervisor(script,
		WithReadyTimeout(45*time.Second), // uv cold-start is ~5s first run
		WithShutdownTimeout(3*time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("bridge Start: %v", err)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = sup.Stop(stopCtx)
	})

	return sup, sup.Client()
}

// ── Happy-path coverage ─────────────────────────────────────────────────────

func TestIntegration_ListDevices(t *testing.T) {
	_, c := startFakeServicesBridge(t)
	devs, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("got %d devices; want 1", len(devs))
	}
	if devs[0].UDID != "00000000-FAKEFAKE0000" {
		t.Errorf("udid = %q; want fake", devs[0].UDID)
	}
}

func TestIntegration_Battery(t *testing.T) {
	_, c := startFakeServicesBridge(t)
	b, err := c.Battery(context.Background(), "00000000-FAKEFAKE0000")
	if err != nil {
		t.Fatalf("Battery: %v", err)
	}
	if b.Level == nil || *b.Level < 0.7 || *b.Level > 0.8 {
		t.Errorf("level = %v; want ~0.77", b.Level)
	}
	if b.Charging == nil || !*b.Charging {
		t.Errorf("charging = %v; want true", b.Charging)
	}
}

func TestIntegration_Screenshot(t *testing.T) {
	_, c := startFakeServicesBridge(t)
	png, err := c.Screenshot(context.Background(), "00000000-FAKEFAKE0000")
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	// Fake header is `\x89PNG\r\n\x1a\nfake`.
	if !strings.HasPrefix(string(png), "\x89PNG") {
		t.Errorf("not a PNG-shaped payload: %q", png[:min(8, len(png))])
	}
}

func TestIntegration_DevicePowerState(t *testing.T) {
	_, c := startFakeServicesBridge(t)
	state, err := c.DevicePowerState(context.Background(), "00000000-FAKEFAKE0000")
	if err != nil {
		t.Fatalf("DevicePowerState: %v", err)
	}
	if state.State != "awake" {
		t.Errorf("state = %q; want \"awake\"", state.State)
	}
}

func TestIntegration_AuthHeaderEnforced(t *testing.T) {
	sup, _ := startFakeServicesBridge(t)
	// Construct a client with the WRONG token and assert the bridge
	// returns 401. This exercises the auth middleware via the real HTTP
	// round-trip.
	bad := NewClient(sup.BaseURL(), "not-the-real-token")
	_, err := bad.ListDevices(context.Background())
	if err == nil {
		t.Fatal("ListDevices with wrong token: expected error; got nil")
	}
	var be *BridgeError
	if !errorsAsBridgeError(err, &be) {
		t.Fatalf("expected *BridgeError; got %T %v", err, err)
	}
	if be.Status != 401 {
		t.Errorf("status = %d; want 401", be.Status)
	}
}

// errorsAsBridgeError wraps errors.As for brevity in this file.
func errorsAsBridgeError(err error, out **BridgeError) bool {
	for e := err; e != nil; {
		if be, ok := e.(*BridgeError); ok {
			*out = be
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := e.(unwrapper); ok {
			e = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

func TestIntegration_CrashReportsList_NDJSONStreaming(t *testing.T) {
	_, c := startFakeServicesBridge(t)
	reports, err := c.CrashReportsList(context.Background(),
		"00000000-FAKEFAKE0000", time.Time{}, "")
	if err != nil {
		t.Fatalf("CrashReportsList: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports; want 3", len(reports))
	}
	for i, r := range reports {
		if r.Process != "FakeApp" {
			t.Errorf("reports[%d].Process = %q; want FakeApp", i, r.Process)
		}
	}
}

func TestIntegration_CrashReportsPull_OctetStreaming(t *testing.T) {
	_, c := startFakeServicesBridge(t)
	content, err := c.CrashReportsPull(context.Background(),
		"00000000-FAKEFAKE0000", "any.ips")
	if err != nil {
		t.Fatalf("CrashReportsPull: %v", err)
	}
	if !strings.Contains(content, "chunk1") || !strings.Contains(content, "end") {
		t.Errorf("content missing expected chunks: %q", content)
	}
	if !strings.Contains(content, "name=any.ips") {
		t.Errorf("content missing passthrough name: %q", content)
	}
}

// ── Induced-failure coverage ────────────────────────────────────────────────

// TestIntegration_SIGKILL_BridgeMidRequest kills the bridge subprocess
// while a request is in flight and asserts the supervisor fires fatal
// with an unexpected-exit message. This is the Jevons-class scenario:
// a real subprocess really dies; the daemon's fail-fast path really
// catches it.
func TestIntegration_SIGKILL_BridgeMidRequest(t *testing.T) {
	script := findDevBridge(t)
	t.Setenv("SPYDER_BRIDGE_FAKE_SERVICES", "1")
	t.Setenv("SPYDER_LOG_LEVEL", "WARNING")

	// Install a fatal hook that captures instead of panicking so this
	// test process survives.
	var captured error
	var fatalOnce sync.Once
	fatalDone := make(chan struct{})
	sup := NewSupervisor(script,
		WithReadyTimeout(45*time.Second),
		WithShutdownTimeout(3*time.Second),
		withFatal(func(err error) {
			fatalOnce.Do(func() {
				captured = err
				close(fatalDone)
			})
		}),
	)

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("bridge Start: %v", err)
	}

	// Kill the bridge's entire process group (uv + python via the dev
	// wrapper). watchdog() should observe the unexpected exit and invoke
	// the fatal hook within its detection window.
	sup.mu.Lock()
	pgid := sup.cmd.Process.Pid
	sup.mu.Unlock()
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL group: %v", err)
	}

	select {
	case <-fatalDone:
		if !strings.Contains(captured.Error(), "subprocess exited unexpectedly") {
			t.Errorf("unexpected fatal message: %v", captured)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fatal hook not called within 10s of SIGKILL")
	}
}
