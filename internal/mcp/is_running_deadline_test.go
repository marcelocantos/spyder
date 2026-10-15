// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/paths"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type blockedStatusAdapter struct {
	*stubAdapter
	invalidated      chan struct{}
	recoveryRelease  chan struct{}
	recoveryFinished chan struct{}
}

func (a *blockedStatusAdapter) InvalidateDevice(_ string) {
	close(a.invalidated)
	<-a.recoveryRelease
	close(a.recoveryFinished)
}

func TestIsRunning_StalledProviderDoesNotHoldGlobalLockOrDelayTimeout(t *testing.T) {
	saved := DeadlineFastRead
	DeadlineFastRead = 80 * time.Millisecond
	t.Cleanup(func() { DeadlineFastRead = saved })
	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.WriteFile(paths.InventoryPath(), []byte(testInventory), 0o600); err != nil {
		t.Fatal(err)
	}
	entered, release, handlerFinished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ios := &blockedStatusAdapter{
		stubAdapter: &stubAdapter{appPID: func(_, _ string) (int, error) {
			close(entered)
			<-release
			return 42, nil
		}},
		invalidated: make(chan struct{}), recoveryRelease: make(chan struct{}), recoveryFinished: make(chan struct{}),
	}
	h := NewHandlerWithAdapters(ios, &stubAdapter{state: func(_ string) (device.State, error) {
		return device.State{ForegroundApp: "com.example.healthy"}, nil
	}})
	h.testHandlers = map[string]toolFunc{"is_running": func(args map[string]any) (*mcpgo.CallToolResult, error) {
		defer close(handlerFinished)
		return h.handleIsRunning(args)
	}}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release); close(ios.recoveryRelease) }) }
	defer unblock()
	result := make(chan *mcpgo.CallToolResult, 1)
	go func() {
		res, _ := h.Dispatch(context.Background(), "is_running", map[string]any{"device": "iPad", "bundle_id": "com.example.app"})
		result <- res
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider never entered")
	}
	// This executes the real cross-device handler while AppPID remains stuck.
	healthy, err := h.Dispatch(context.Background(), "device_state", map[string]any{"device": "Raspberry"})
	if err != nil || healthy == nil || healthy.IsError {
		t.Fatalf("other device blocked: %v %+v", err, healthy)
	}
	select {
	case res := <-result:
		if res == nil || !res.IsError {
			t.Fatalf("expected structured deadline result: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline response waited for provider/recovery")
	}
	// No onDeviceTimeout substitution: the production resolver and adapter
	// invalidation path runs and deliberately remains blocked itself.
	select {
	case <-ios.invalidated:
	case <-time.After(time.Second):
		t.Fatal("actual invalidation did not start")
	}
	select {
	case <-ios.recoveryFinished:
		t.Fatal("recovery unexpectedly unblocked")
	default:
	}
	healthy, err = h.Dispatch(context.Background(), "device_state", map[string]any{"device": "Raspberry"})
	if err != nil || healthy == nil || healthy.IsError {
		t.Fatalf("blocked recovery stalled healthy read: %v %+v", err, healthy)
	}
	unblock()
	for _, finished := range []<-chan struct{}{handlerFinished, ios.recoveryFinished} {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("test provider/recovery goroutine did not finish")
		}
	}
}

func TestDispatch_ActualInvalidationWaitsForHandlerLockOutsideResponse(t *testing.T) {
	saved := DeadlineFastRead
	DeadlineFastRead = 40 * time.Millisecond
	t.Cleanup(func() { DeadlineFastRead = saved })
	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.WriteFile(filepath.Join(paths.Base(), "inventory.json"), []byte(testInventory), 0o600); err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ios := &blockedStatusAdapter{stubAdapter: &stubAdapter{}, invalidated: make(chan struct{}),
		recoveryRelease: make(chan struct{}), recoveryFinished: make(chan struct{})}
	close(ios.recoveryRelease)
	h := NewHandlerWithAdapters(ios, &stubAdapter{})
	h.testHandlers = map[string]toolFunc{"is_running": func(_ map[string]any) (*mcpgo.CallToolResult, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		defer close(finished)
		close(entered)
		<-release
		return toolText("released")
	}}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	result := make(chan *mcpgo.CallToolResult, 1)
	go func() {
		res, _ := h.Dispatch(context.Background(), "is_running", map[string]any{"device": "iPad"})
		result <- res
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler never took lock")
	}
	select {
	case res := <-result:
		if res == nil || !res.IsError {
			t.Fatalf("expected timeout: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout invalidation waited for h.mu")
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler goroutine did not finish")
	}
	select {
	case <-ios.recoveryFinished:
	case <-time.After(time.Second):
		t.Fatal("actual invalidation goroutine did not finish")
	}
}

// Legitimate slow installs were triggering unrelated FastRead deadlines and
// daemon self-restart. Exercise the actual lifecycle handlers, not a substitute
// handler that accidentally avoids their lock scope.
func TestLifecycle_SlowProviderLeavesOtherDeviceReadsResponsive(t *testing.T) {
	for _, verb := range []string{"install_app", "deploy_app", "launch_app", "terminate_app"} {
		t.Run(verb, func(t *testing.T) {
			saved := DeadlineFastRead
			DeadlineFastRead = 40 * time.Millisecond
			t.Cleanup(func() { DeadlineFastRead = saved })
			t.Setenv(paths.DataDirEnv, t.TempDir())
			if err := os.WriteFile(paths.InventoryPath(), []byte(testInventory), 0o600); err != nil {
				t.Fatal(err)
			}
			appPath := filepath.Join(t.TempDir(), "Fixture.app")
			if err := os.Mkdir(appPath, 0o700); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			slow := func(_, _ string) error { close(entered); <-release; return nil }
			ios := &stubAdapter{appPID: func(_, _ string) (int, error) { return 42, nil }}
			switch verb {
			case "install_app", "deploy_app":
				ios.installApp = slow
			case "launch_app":
				ios.launchApp = func(id, bundle string, _ map[string]string) error { return slow(id, bundle) }
			case "terminate_app":
				ios.terminateApp = slow
				ios.appPID = func(_, _ string) (int, error) { return 0, nil }
			}
			h := NewHandlerWithAdapters(ios, &stubAdapter{state: func(_ string) (device.State, error) { return device.State{ForegroundApp: "com.example.healthy"}, nil }})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			finished := make(chan *mcpgo.CallToolResult, 1)
			go func() {
				res, _ := h.Dispatch(context.Background(), verb, map[string]any{"device": "iPad", "path": appPath, "bundle_id": "com.example.app"})
				finished <- res
			}()
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("lifecycle provider did not enter")
			}
			for range 2 {
				result, err := h.Dispatch(context.Background(), "device_state", map[string]any{"device": "Raspberry"})
				if err != nil || result == nil || result.IsError {
					t.Fatalf("%s blocked other device: %v %+v", verb, err, result)
				}
			}
			unblock()
			select {
			case result := <-finished:
				if result == nil || result.IsError {
					t.Fatalf("released lifecycle failed: %+v", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("lifecycle goroutine did not finish")
			}
		})
	}
}
