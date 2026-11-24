// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// A blocking verify whose caller dies (a killed `spyder verify`) must
// return at once, keep the run going, and never ask the daemon to
// restart. On 2026-11-22 it stayed blocked in Wait, the stuck-dispatch
// watchdog fired, and the daemon self-restarted.
func TestVerify_CallerGoneDetachesWithoutSelfRestart(t *testing.T) {
	h := NewHandler()
	h.EnableSelfHeal(time.Hour, 20*time.Millisecond)
	h.selfRestartGrace = 40 * time.Millisecond
	var exits atomic.Int32
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits.Add(1) })

	wf := `
name: blocks
steps:
  - id: ask
    type: human_gate
    prompt: Look
    choices:
      - id: pass
        label: Yes
`
	ctx, cancel := context.WithCancel(context.Background())
	cwd := t.TempDir()
	done := make(chan struct{})
	go func() {
		_, _ = h.Dispatch(ctx, "verify", map[string]any{"workflow": wf, "cwd": cwd, "wait": true})
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("verify dispatch stayed blocked after its caller went away")
	}
	time.Sleep(200 * time.Millisecond)
	if n := exits.Load(); n != 0 {
		t.Fatalf("caller going away requested %d daemon self-restarts", n)
	}
	// The run carries on: it is still waiting on its gate.
	status := dispatchJSONMap(t, h, "verify_status", map[string]any{})
	body, _ := json.Marshal(status)
	if !strings.Contains(string(body), "blocks") {
		t.Fatalf("detached run is gone from verify_status: %v", status)
	}
}

// A dispatch whose caller disconnects is not a wedged daemon, even when
// the handler ignores its context for a while.
func TestDispatch_CallerGoneIsNotAStuckDispatch(t *testing.T) {
	h := NewHandler()
	h.EnableSelfHeal(time.Hour, 20*time.Millisecond)
	h.selfRestartGrace = 30 * time.Millisecond
	var exits atomic.Int32
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits.Add(1) })
	release := make(chan struct{})
	h.testHandlers = map[string]toolFunc{
		"devices": func(map[string]any) (*mcpgo.CallToolResult, error) { <-release; return toolText("ok") },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = h.Dispatch(ctx, "devices", map[string]any{}); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	time.Sleep(150 * time.Millisecond) // well past selfRestartGrace
	close(release)
	if n := exits.Load(); n != 0 {
		t.Fatalf("caller cancellation requested %d daemon self-restarts", n)
	}
}

// The simulator says "<bundle> is not running" once the app is gone;
// terminate_app must read that as success, not as a failed check.
func TestTerminateApp_SimulatorWordingIsSuccess(t *testing.T) {
	var gone atomic.Bool
	ios := &stubAdapter{
		terminateApp: func(_, _ string) error { gone.Store(true); return nil },
		appPID: func(_, bundle string) (int, error) {
			if gone.Load() {
				return 0, errors.New(bundle + " is not running")
			}
			return 9, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)
	r := dispatchJSON(t, h, "terminate_app", map[string]any{"device": "iPad", "bundle_id": "com.example.app"})
	if r.IsError {
		t.Fatalf("terminate_app failed after the app was gone: %s", resultText(t, &r))
	}
}

// terminate_app means "ensure the app is not running". Each adapter's
// wording for an app that is already gone is success, with no
// is_running guard needed; real failures still fail.
func TestTerminateApp_AlreadyGoneIsSuccessOnEveryAdapter(t *testing.T) {
	const bundle = "com.example.app"
	cases := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"ios/android/desktop", errors.New("app not running: " + bundle), false},
		{"simulator", errors.New(bundle + " is not running"), false},
		{"wrapped", fmt.Errorf("kill: %w", errors.New("app not running: "+bundle)), false},
		{"disconnected", errors.New("device not connected"), true},
		{"tunnel down", errors.New("ios tunnel not running"), true},
		{"other app gone", errors.New("com.other.app is not running"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var pidCalls atomic.Int32
			ios := &stubAdapter{
				terminateApp: func(_, _ string) error { return c.err },
				appPID: func(_, _ string) (int, error) {
					pidCalls.Add(1)
					return 9, nil // would fail the stop check if reached
				},
			}
			h := newHandlerWithStubs(t, ios, nil)
			r := dispatchJSON(t, h, "terminate_app", map[string]any{"device": "iPad", "bundle_id": bundle})
			if r.IsError != c.wantErr {
				t.Fatalf("IsError = %v, want %v: %s", r.IsError, c.wantErr, resultText(t, &r))
			}
			if !c.wantErr {
				if got := resultText(t, &r); !strings.Contains(got, "already not running") {
					t.Fatalf("result = %q, want it to say the app was already not running", got)
				}
				if n := pidCalls.Load(); n != 0 {
					t.Fatalf("already-gone app still ran %d pid checks", n)
				}
			}
		})
	}
}
