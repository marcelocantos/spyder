// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/paths"
)

// 🎯T171: a hung adb screencap must return at the dispatch deadline and must
// not trip daemon-wide self-restart while other devices keep working.
func TestDispatch_AndroidHungScreencap_NoSelfRestart(t *testing.T) {
	saveD := DeadlineDeviceOp
	DeadlineDeviceOp = 80 * time.Millisecond
	t.Cleanup(func() { DeadlineDeviceOp = saveD })

	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.WriteFile(paths.InventoryPath(), []byte(testInventory), 0o600); err != nil {
		t.Fatal(err)
	}

	restore := device.SetAndroidAdbForTest(func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		if len(args) >= 5 && args[2] == "shell" && args[3] == "screencap" {
			cmd := exec.CommandContext(ctx, "sleep", "99999")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			err := cmd.Run()
			return nil, nil, err
		}
		return nil, nil, exec.ErrNotFound
	})
	t.Cleanup(restore)

	ios := &stubAdapter{state: func(string) (device.State, error) {
		return device.State{ForegroundApp: "com.example.healthy"}, nil
	}}
	h := NewHandlerWithAdapters(ios, device.NewAndroidAdapter())
	h.EnableSelfHeal(time.Hour, 40*time.Millisecond)
	var exits atomic.Int32
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits.Add(1) })

	started := time.Now()
	res, err := h.Dispatch(context.Background(), "screenshot", map[string]any{"device": "Raspberry"})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Dispatch transport err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("want timeout error result, got %+v", res)
	}
	text := resultErrorText(t, res)
	if !strings.Contains(text, "tool timeout") || !strings.Contains(text, "screenshot") {
		t.Fatalf("unexpected body: %s", text)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("screenshot hung too long: %v", elapsed)
	}

	healthy, err := h.Dispatch(context.Background(), "device_state", map[string]any{"device": "iPad"})
	if err != nil || healthy == nil || healthy.IsError {
		t.Fatalf("other device blocked: %v %+v", err, healthy)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if exits.Load() > 0 {
			t.Fatal("self-restart fired for cancellable adb screencap hang")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
