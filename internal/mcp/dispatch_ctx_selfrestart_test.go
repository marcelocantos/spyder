// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/paths"
)

// The supervised daemon exited with status 1 overnight (2026-11-11 and
// earlier): system_alert_tap, run_script and wait_app_session ran past their
// dispatch deadline because they ignored the dispatch context, and the
// 🎯T99.3 watchdog treated the still-running handler as a wedge and exited.
// These tests drive each handler past a short deadline and require it to
// return within the restart grace, so the exit never fires.

// selfHealHandler returns a handler with self-heal on and a counting exit.
func selfHealHandler(t *testing.T, ios device.Adapter) (*Handler, *atomic.Int32) {
	t.Helper()
	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.WriteFile(paths.InventoryPath(), []byte(testInventory), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(ios, nil)
	h.EnableSelfHeal(time.Hour, 100*time.Millisecond)
	var exits atomic.Int32
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits.Add(1) })
	return h, &exits
}

// requireNoSelfRestart waits past the restart grace and fails on any exit.
func requireNoSelfRestart(t *testing.T, exits *atomic.Int32) {
	t.Helper()
	until := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(until) {
		if exits.Load() > 0 {
			t.Fatal("self-restart fired: the handler outlived its dispatch deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// slowAlertStub shows an alert and taps only when its context ends, like a
// device whose alert runner is held up by a concurrent install.
type slowAlertStub struct {
	*stubAdapter
	tapReturned atomic.Bool
}

func (s *slowAlertStub) SystemAlert(context.Context, string) (device.SystemAlert, error) {
	return localNetwork, nil
}

func (s *slowAlertStub) TapSystemAlertButton(ctx context.Context, _, _, _ string) (device.SystemAlert, device.SystemAlert, error) {
	<-ctx.Done()
	s.tapReturned.Store(true)
	return localNetwork, device.SystemAlert{}, ctx.Err()
}

func TestDispatch_SystemAlertTapPastDeadline_NoSelfRestart(t *testing.T) {
	save := DeadlineSystemAlertTap
	DeadlineSystemAlertTap = 80 * time.Millisecond
	t.Cleanup(func() { DeadlineSystemAlertTap = save })

	stub := &slowAlertStub{stubAdapter: &stubAdapter{}}
	h, exits := selfHealHandler(t, stub)
	res, err := h.Dispatch(context.Background(), "system_alert_tap", map[string]any{"device": "iPad", "button": "Allow"})
	if err != nil || res == nil || !res.IsError || !strings.Contains(resultErrorText(t, res), "tool timeout") {
		t.Fatalf("want a tool timeout, got %v %+v", err, res)
	}
	requireNoSelfRestart(t, exits)
	if !stub.tapReturned.Load() {
		t.Fatal("tap never saw its context end")
	}
}

func TestDispatch_WaitAppSessionPastDeadline_NoSelfRestart(t *testing.T) {
	save := DeadlineWaitState
	DeadlineWaitState = 80 * time.Millisecond
	t.Cleanup(func() { DeadlineWaitState = save })

	h, exits := selfHealHandler(t, &stubAdapter{})
	h.appChannel = appchannel.NewManager()
	// timeout_ms far beyond the dispatch deadline, as overnight scripts used.
	res, err := h.Dispatch(context.Background(), "wait_app_session", map[string]any{
		"device": "iPad", "bundle_id": "com.example.game", "timeout_ms": 600000.0,
	})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("want an error result, got %v %+v", err, res)
	}
	requireNoSelfRestart(t, exits)
}

func TestAppExec_WaitAppSessionEndsWithScript(t *testing.T) {
	h, exits := selfHealHandler(t, &stubAdapter{})
	h.appChannel = appchannel.NewManager()
	started := time.Now()
	res, err := h.Dispatch(context.Background(), "app_exec", map[string]any{
		"script":          `wait_app_session(device="iPad", bundle_id="com.example.game", timeout_ms=600000)`,
		"max_duration_ms": 150.0,
	})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("want the script to fail at its budget, got %v %+v", err, res)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("wait_app_session ignored the script context: %v", elapsed)
	}
	requireNoSelfRestart(t, exits)
}

func TestRunScript_EndsWithCallerContext(t *testing.T) {
	h, exits := selfHealHandler(t, &stubAdapter{})
	script := filepath.Join(t.TempDir(), "long.star")
	if err := os.WriteFile(script, []byte("sleep(ms=600000)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	res, err := h.dispatch(ctx, "run_script", map[string]any{"path": script, "max_duration_ms": 600000.0})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("want the script to stop with its caller, got %v %+v", err, res)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("run_script ignored the caller context: %v", elapsed)
	}
	requireNoSelfRestart(t, exits)
}
