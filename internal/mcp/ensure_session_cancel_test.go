// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/paths"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func cancelSessionHandler(t *testing.T, ios *stubAdapter) *Handler {
	t.Helper()
	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.WriteFile(paths.InventoryPath(), []byte(testInventory), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(ios, &stubAdapter{})
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	return h
}

// This is the actual dispatch/lifecycle/recovery path from the retained A23
// dump: the caller cancels a longer handshake wait, then needs to terminate
// the same device. A successful timeout response alone cannot decide this.
func TestEnsureSession_CancellationReleasesGateWithoutSelfRestart(t *testing.T) {
	for _, transport := range []string{"direct", "starlark"} {
		t.Run(transport, func(t *testing.T) {
			var launched atomic.Bool
			verified := make(chan struct{}, 1)
			var terminated atomic.Int32
			ios := &stubAdapter{
				appPID: func(_, _ string) (int, error) {
					if !launched.Load() {
						return 0, errors.New("app not running")
					}
					select {
					case verified <- struct{}{}:
					default:
					}
					return 42, nil
				},
				launchApp:    func(_, _ string, _ map[string]string) error { launched.Store(true); return nil },
				terminateApp: func(_, _ string) error { terminated.Add(1); launched.Store(false); return nil },
			}
			h := cancelSessionHandler(t, ios)
			h.EnableSelfHeal(time.Hour, 20*time.Millisecond)
			h.selfRestartGrace = 40 * time.Millisecond
			var exits atomic.Int32
			h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits.Add(1) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan *mcpgo.CallToolResult, 1)
			args := map[string]any{"device": "iPad", "bundle_id": "com.example.app", "timeout_ms": float64(600)}
			verb := "ensure_session"
			if transport == "starlark" {
				verb = "app_exec"
				args = map[string]any{"script": `ensure_session(device="iPad", bundle_id="com.example.app", timeout_ms=600)`, "max_duration_ms": float64(2000)}
			}
			go func() { r, _ := h.Dispatch(ctx, verb, args); done <- r }()
			select {
			case <-verified:
			case <-time.After(time.Second):
				t.Fatal("launch did not reach PID verification")
			}
			cancel()
			select {
			case r := <-done:
				if r == nil || !r.IsError {
					t.Fatalf("cancelled call returned success: %+v", r)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("caller cancellation blocked")
			}
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer stopCancel()
			result, err := h.Dispatch(stopCtx, "terminate_app", map[string]any{"device": "iPad", "bundle_id": "com.example.app"})
			// Even a negative-control failure must allow the finite original wait to
			// unwind, rather than leaking a live handler into subsequent tests.
			t.Cleanup(func() {
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					h.lifecycleMu.Lock()
					n := len(h.lifecycleGates)
					h.lifecycleMu.Unlock()
					if n == 0 {
						return
					}
					time.Sleep(time.Millisecond)
				}
				t.Error("lifecycle handler did not drain")
			})
			if err != nil || result == nil || result.IsError {
				t.Fatalf("same-device terminate was blocked after cancellation: %v %+v", err, result)
			}
			if terminated.Load() != 1 {
				t.Fatalf("terminate calls=%d, want1", terminated.Load())
			}
			status, err := h.Dispatch(context.Background(), "device_state", map[string]any{"device": "Raspberry"})
			if err != nil || status == nil || status.IsError {
				t.Fatalf("other-device status failed: %v %+v", err, status)
			}
			time.Sleep(80 * time.Millisecond)
			if exits.Load() != 0 {
				t.Fatalf("cancelled finite wait requested %d daemon exits", exits.Load())
			}
		})
	}
}

func TestEnsureSession_ChannelHealthAndReadinessInheritCancellation(t *testing.T) {
	h := cancelSessionHandler(t, &stubAdapter{})
	key := appchannel.AppKey{DeviceID: "00008103-001122334455667A", BundleID: "com.example.app"}
	listener, err := h.appChannel.GetOrCreateListener(key)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listener.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello, err := appchannel.PackParams(appchannel.Hello{AppName: "silent", Methods: appchannel.MethodDescriptors([]string{appchannel.MethodPing, appchannel.MethodStateQuery}...)})
	if err != nil {
		t.Fatal(err)
	}
	if err = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: hello}); err != nil {
		t.Fatal(err)
	}
	if _, err = appchannel.ReadFrame(conn); err != nil {
		t.Fatal(err)
	}
	session, ok := h.waitForChannelSessionContext(context.Background(), key, time.Time{}, 200*time.Millisecond)
	if !ok {
		t.Fatal("acknowledged handshake did not register a session")
	}
	for _, kind := range []string{"health", "readiness"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			started := time.Now()
			var healthy bool
			if kind == "health" {
				healthy = sessionHealthy(ctx, session)
			} else {
				healthy = waitForSessionReady(ctx, session, time.Second)
			}
			if healthy || time.Since(started) > 200*time.Millisecond {
				t.Fatalf("%s ignored caller cancellation", kind)
			}
		})
	}
}

func TestEnsureSession_CancelledPIDPollingMakesNoFurtherProviderCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	adapter := &stubAdapter{appPID: func(_, _ string) (int, error) { calls.Add(1); return 42, nil }}
	if _, err := waitForAppPIDContext(ctx, adapter, "id", "bundle", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("PID wait error: %v", err)
	}
	h := NewHandlerWithAdapters(adapter, &stubAdapter{})
	if err := h.waitUntilNotRunningContext(ctx, adapter, "id", "bundle", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("termination wait error: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("cancelled waits made %d provider calls", calls.Load())
	}
}

func TestEnsureSession_AlreadyCancelledDoesNotLaunch(t *testing.T) {
	var launches atomic.Int32
	h := cancelSessionHandler(t, &stubAdapter{launchApp: func(_, _ string, _ map[string]string) error { launches.Add(1); return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := h.handleEnsureSessionContext(ctx, map[string]any{"device": "iPad", "bundle_id": "com.example.app"})
	if err != nil || r == nil || !r.IsError || !strings.Contains(resultErrorText(t, r), "cancelled") {
		t.Fatalf("cancel result: %v %+v", err, r)
	}
	if launches.Load() != 0 {
		t.Fatal("cancelled session setup launched the app")
	}
}
