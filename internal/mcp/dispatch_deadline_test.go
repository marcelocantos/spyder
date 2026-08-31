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

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/health"
)

// TestDispatch_TimeoutReturnsStructuredError is the 🎯T99.1 class-1 oracle:
// a never-returning handler under a short tool-class deadline yields a
// structured timeout error and does not require killing the process.
func TestDispatch_TimeoutReturnsStructuredError(t *testing.T) {
	save := DeadlineDeviceOp
	DeadlineDeviceOp = 40 * time.Millisecond
	t.Cleanup(func() { DeadlineDeviceOp = save })

	h := NewHandler()
	h.testHandlers = map[string]toolFunc{
		"screenshot": func(args map[string]any) (*mcpgo.CallToolResult, error) {
			time.Sleep(5 * time.Second)
			return toolText("should-not-return")
		},
	}

	var invalidated atomic.Int32
	h.onDeviceTimeout = func(device string) {
		if device == "Jevons" {
			invalidated.Add(1)
		}
	}

	started := time.Now()
	res, err := h.Dispatch(context.Background(), "screenshot", map[string]any{"device": "Jevons"})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Dispatch returned transport err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("want IsError timeout result, got %+v", res)
	}
	text := resultErrorText(t, res)
	if !strings.Contains(text, "tool timeout") || !strings.Contains(text, "screenshot") {
		t.Fatalf("timeout text: %q", text)
	}
	if !strings.Contains(text, "Jevons") {
		t.Fatalf("timeout should name device: %q", text)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
	if invalidated.Load() != 1 {
		t.Fatalf("device session invalidate count = %d want 1", invalidated.Load())
	}

	// Subsequent call on a normal tool succeeds without process kill.
	h.testHandlers = nil
	res2, err := h.Dispatch(context.Background(), "devices", map[string]any{})
	if err != nil {
		t.Fatalf("subsequent Dispatch: %v", err)
	}
	if res2 == nil || res2.IsError {
		t.Fatalf("subsequent call failed: %+v", res2)
	}
}

// TestDispatch_StuckTriggersSelfRestart is the 🎯T99.3 shipped-path oracle:
// a never-returning handler through real Dispatch + EnableSelfHeal must, after
// tool deadline + grace: (1) flip entity "spyder" to needs_attention via
// ForceStall while still outstanding, (2) fire the BeforeExit dump hook, and
// (3) call exitFn. Unit Begin+ForceStall alone is not sufficient — Done after
// timeout previously no-op'd ForceStall on this path.
func TestDispatch_StuckTriggersSelfRestart(t *testing.T) {
	saveD := DeadlineDeviceOp
	DeadlineDeviceOp = 30 * time.Millisecond
	t.Cleanup(func() { DeadlineDeviceOp = saveD })

	h := NewHandler()
	h.EnableSelfHeal(time.Hour, 20*time.Millisecond) // long watchdog: only ForceStall path
	dumps := 0
	exits := 0
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits++ })
	h.selfRestart.SetBeforeExit(func(string) { dumps++ })

	h.testHandlers = map[string]toolFunc{
		"screenshot": func(args map[string]any) (*mcpgo.CallToolResult, error) {
			time.Sleep(5 * time.Second)
			return toolText("never")
		},
	}

	res, err := h.Dispatch(context.Background(), "screenshot", map[string]any{"device": "Jevons"})
	if err != nil {
		t.Fatalf("Dispatch transport err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("want timeout IsError, got %+v", res)
	}

	// Allow grace + completeStuckDispatch (ForceStall + Request).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if exits >= 1 && dumps >= 1 && spyderNeedsAttention(h) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("shipped stuck path incomplete: exits=%d dumps=%d needs_attention=%v entities=%+v",
		exits, dumps, spyderNeedsAttention(h), h.Health().Model().Snapshot().Entities)
}

func spyderNeedsAttention(h *Handler) bool {
	if h == nil || h.Health() == nil {
		return false
	}
	for _, e := range h.Health().Model().Snapshot().Entities {
		if e.ID.Name == "spyder" && string(e.State) == "needs_attention" {
			return true
		}
	}
	return false
}

func TestLaunchPlayer_DeadlineCoversInstall(t *testing.T) {
	if toolDeadlineClass("launch_player") != DeadlineInstall {
		t.Fatalf("launch_player deadline %v want DeadlineInstall %v",
			toolDeadlineClass("launch_player"), DeadlineInstall)
	}
}

// TestDispatch_SlowInstallDoesNotStallWatchdog is the 🎯T103 shipped-path
// oracle: an InstallApp longer than the stall window stays healthy because
// installOn beats the watchdog. The stall timeout is not raised.
func TestDispatch_SlowInstallDoesNotStallWatchdog(t *testing.T) {
	save := installProgressTick
	installProgressTick = 20 * time.Millisecond
	t.Cleanup(func() { installProgressTick = save })

	tmp := t.TempDir()
	appPath := filepath.Join(tmp, "MyApp.app")
	if err := os.MkdirAll(appPath, 0o755); err != nil {
		t.Fatal(err)
	}

	ios := &stubAdapter{installApp: func(id, path string) error {
		time.Sleep(300 * time.Millisecond)
		return nil
	}}
	h := newHandlerWithStubs(t, ios, nil)
	h.EnableSelfHeal(80*time.Millisecond, time.Hour)
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) {
		t.Error("self-restart must not fire for a slow install")
	})

	var stalled atomic.Bool
	h.Health().Model().OnTransition(func(tr health.Transition) {
		if tr.ID.Name == "spyder" && tr.To == health.NeedsAttention {
			stalled.Store(true)
		}
	})

	res, err := h.Dispatch(context.Background(), "install_app", map[string]any{
		"device": "iPad",
		"path":   appPath,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("install_app error: %s", resultErrorText(t, res))
	}
	if stalled.Load() {
		t.Fatal("install beats should keep the watchdog healthy")
	}
}

// TestDispatch_AppExecSleepDoesNotStallWatchdog is the 🎯T103.1 shipped-path
// oracle: a script that only sleep()s longer than the stall window stays
// healthy because sleep ticks Beat the watchdog. The stall timeout is not
// raised.
func TestDispatch_AppExecSleepDoesNotStallWatchdog(t *testing.T) {
	save := execProgressTick
	execProgressTick = 20 * time.Millisecond
	t.Cleanup(func() { execProgressTick = save })

	h := NewHandler()
	h.EnableSelfHeal(80*time.Millisecond, time.Hour)
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) {
		t.Error("self-restart must not fire for a sleeping script")
	})

	var stalled atomic.Bool
	h.Health().Model().OnTransition(func(tr health.Transition) {
		if tr.ID.Name == "spyder" && tr.To == health.NeedsAttention {
			stalled.Store(true)
		}
	})

	res, err := h.Dispatch(context.Background(), "app_exec", map[string]any{
		"script":          "sleep(300)",
		"max_duration_ms": float64(2000),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("app_exec error: %s", resultErrorText(t, res))
	}
	if stalled.Load() {
		t.Fatal("sleep ticks should beat the watchdog; got needs_attention")
	}
}

// TestDispatch_InFlightOpsListsActiveCall covers 🎯T99.5 op registry.
func TestDispatch_InFlightOpsListsActiveCall(t *testing.T) {
	save := DeadlineDeviceOp
	DeadlineDeviceOp = 2 * time.Second
	t.Cleanup(func() { DeadlineDeviceOp = save })

	h := NewHandler()
	entered := make(chan struct{})
	release := make(chan struct{})
	h.testHandlers = map[string]toolFunc{
		"screenshot": func(args map[string]any) (*mcpgo.CallToolResult, error) {
			close(entered)
			<-release
			return toolText("ok")
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.Dispatch(context.Background(), "screenshot", map[string]any{"device": "Pixel"})
	}()
	<-entered
	ops := h.InFlightOps()
	if len(ops) != 1 {
		t.Fatalf("in-flight ops: got %d want 1: %+v", len(ops), ops)
	}
	if ops[0].Tool != "screenshot" || ops[0].Device != "Pixel" {
		t.Fatalf("op snapshot: %+v", ops[0])
	}
	close(release)
	<-done
	if len(h.InFlightOps()) != 0 {
		t.Fatalf("ops after done: %+v", h.InFlightOps())
	}
}

func resultErrorText(t *testing.T, res *mcpgo.CallToolResult) string {
	t.Helper()
	if res == nil {
		return ""
	}
	for _, c := range res.Content {
		switch v := c.(type) {
		case mcpgo.TextContent:
			return v.Text
		}
	}
	// mcp-go may encode as pointer or via Content interface.
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(mcpgo.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
