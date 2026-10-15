// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type countInvalidationAdapter struct {
	*stubAdapter
	invalidations atomic.Int32
}

func (a *countInvalidationAdapter) InvalidateDevice(_ string) { a.invalidations.Add(1) }

func TestLifecycle_CanonicalDevicePipelineAtomicAndCancelledWaiterNeverExecutes(t *testing.T) {
	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.WriteFile(paths.InventoryPath(), []byte(testInventory), 0o600); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(t.TempDir(), "Fixture.app")
	if err := os.Mkdir(appPath, 0o700); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var mu sync.Mutex
	var stages []string
	record := func(stage string) { mu.Lock(); stages = append(stages, stage); mu.Unlock() }
	var installs, terminates atomic.Int32
	var launched atomic.Bool
	ios := &countInvalidationAdapter{stubAdapter: &stubAdapter{
		installApp: func(_, _ string) error {
			if installs.Add(1) == 1 {
				record("deploy-install")
				close(entered)
				<-release
			} else {
				record("second-install")
			}
			return nil
		},
		terminateApp: func(_, _ string) error { terminates.Add(1); return nil },
		launchApp: func(_, _ string, _ map[string]string) error {
			record("deploy-launch")
			launched.Store(true)
			return nil
		},
		appPID: func(_, _ string) (int, error) {
			if launched.Load() {
				record("deploy-pid")
				return 42, nil
			}
			return 0, errors.New("app not running")
		},
	}}
	h := NewHandlerWithAdapters(ios, &stubAdapter{})
	deployDone := make(chan *mcpgo.CallToolResult, 1)
	go func() {
		res, _ := h.Dispatch(context.Background(), "deploy_app", map[string]any{"device": "iPad", "path": appPath, "bundle_id": "com.example.app", "owner": "office"})
		deployDone <- res
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("deploy did not reach install")
	}
	initialTerminates := terminates.Load()
	statusContext, cancelStatus := context.WithTimeout(context.Background(), 60*time.Millisecond)
	status, statusErr := h.Dispatch(statusContext, "is_running", map[string]any{"device": "iPad", "bundle_id": "com.example.app"})
	cancelStatus()
	if statusErr != nil || status == nil || !status.IsError {
		t.Fatalf("same-device status must wait for install without invalidation: %v %+v", statusErr, status)
	}
	// Alias and hardware UUID must resolve to the same pipeline gate.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	res, err := h.Dispatch(ctx, "terminate_app", map[string]any{"device": "00008103-001122334455667A", "bundle_id": "com.example.app", "owner": "office"})
	cancel()
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("queued deadline should return error: %v %+v", err, res)
	}
	// Starlark's direct builtin handler bridge must use that same gate/context.
	res, err = h.handleAppExecContext(context.Background(), map[string]any{
		"script":          `terminate_app(device="00008103-001122334455667A", bundle_id="com.example.app", owner="office")`,
		"max_duration_ms": float64(60),
	})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("queued Starlark deadline should fail: %v %+v", err, res)
	}
	if terminates.Load() != initialTerminates {
		t.Fatal("queued terminate interleaved with deploy")
	}
	if ios.invalidations.Load() != 0 {
		t.Fatal("queued timeout invalidated active install provider")
	}
	secondDone := make(chan *mcpgo.CallToolResult, 1)
	go func() {
		res, _ := h.Dispatch(context.Background(), "install_app", map[string]any{"device": "00008103-001122334455667A", "path": appPath, "owner": "office"})
		secondDone <- res
	}()
	deadline := time.Now().Add(time.Second)
	queued := false
	for time.Now().Before(deadline) {
		h.lifecycleMu.Lock()
		for _, gate := range h.lifecycleGates {
			if gate.references >= 2 {
				queued = true
			}
		}
		h.lifecycleMu.Unlock()
		if queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !queued {
		t.Fatal("second lifecycle call never queued")
	}
	if installs.Load() != 1 {
		t.Fatal("same-device install interleaved with deploy")
	}
	unblock()
	for _, done := range []<-chan *mcpgo.CallToolResult{deployDone, secondDone} {
		select {
		case result := <-done:
			if result == nil || result.IsError {
				t.Fatalf("released lifecycle failed: %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("lifecycle goroutine did not finish")
		}
	}
	mu.Lock()
	order := strings.Join(stages, ",")
	mu.Unlock()
	if order != "deploy-install,deploy-launch,deploy-pid,second-install" {
		t.Fatalf("pipeline interleaved: %s", order)
	}
	if terminates.Load() != initialTerminates {
		t.Fatal("cancelled waiter executed late")
	}
	h.lifecycleMu.Lock()
	remaining := len(h.lifecycleGates)
	h.lifecycleMu.Unlock()
	if remaining != 0 {
		t.Fatalf("lifecycle gate leaked after all callers finished: %d", remaining)
	}
}

// Real desktop backend fixture: public Dispatch drives an owned child process,
// never an installed/user application. Keep this in the regular test gate.
func TestLifecycle_DesktopFixtureProcessThroughDispatch(t *testing.T) {
	t.Setenv(paths.DataDirEnv, t.TempDir())
	executable := filepath.Join(t.TempDir(), "fixture-game.sh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'ready\\n'\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]map[string]string{{"alias": "FixtureDesktop", "platform": "desktop", "executable_path": executable}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.InventoryPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(&stubAdapter{}, &stubAdapter{})
	args := map[string]any{"device": "FixtureDesktop", "bundle_id": "com.test.lifecycle-fixture"}
	t.Cleanup(func() { _, _ = h.handleTerminateApp(args) })
	for _, verb := range []string{"launch_app", "is_running", "terminate_app"} {
		result, err := h.Dispatch(context.Background(), verb, args)
		if err != nil || result == nil || result.IsError {
			t.Fatalf("desktop %s: %v %+v", verb, err, result)
		}
		if verb == "is_running" {
			var running isRunningResult
			if err := json.Unmarshal([]byte(resultErrorText(t, result)), &running); err != nil || running.State != "running" || running.PID <= 0 {
				t.Fatalf("desktop PID not observed: %+v", result)
			}
		}
	}
	args["device"] = "fixturedesktop" // case-insensitive alias, same canonical process
	result, err := h.Dispatch(context.Background(), "is_running", args)
	var stopped isRunningResult
	if result != nil {
		_ = json.Unmarshal([]byte(resultErrorText(t, result)), &stopped)
	}
	if err != nil || result == nil || result.IsError || (stopped.State != "not_running" && stopped.State != "not_installed") {
		t.Fatalf("desktop fixture still running: %v %+v", err, result)
	}
	if _, pidErr := h.desktop.AppPID(executable, "com.test.lifecycle-fixture"); pidErr == nil {
		t.Fatal("real desktop fixture process still exists")
	}
}
