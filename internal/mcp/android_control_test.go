// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T111 — MCP entry points for perf_fps, port_forward_*, input_tap/swipe.

package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

func TestT111ToolsDispatchKnown(t *testing.T) {
	h := newTestHandler(t)
	for _, name := range []string{
		"perf_fps", "port_forward_start", "port_forward_stop", "port_forward_list",
		"input_tap", "input_swipe",
	} {
		_, err := h.Dispatch(context.Background(), name, map[string]any{
			"device": "x", "package": "p", "device_port": 1.0, "local_port": 1.0,
			"x": 1.0, "y": 1.0, "x1": 1.0, "y1": 1.0, "x2": 2.0, "y2": 2.0,
		})
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("unknown tool %q: %v", name, err)
		}
	}
}

func TestPerfFPS_AndroidPath(t *testing.T) {
	var gotID, gotPkg string
	var gotWin time.Duration
	android := &stubAdapter{
		measureFrameStats: func(id, packageName string, window time.Duration) (device.FrameStats, error) {
			gotID, gotPkg, gotWin = id, packageName, window
			return device.FrameStats{
				Package: packageName, WindowSec: window.Seconds(),
				TotalFrames: 120, FPS: 60, Source: "gfxinfo",
			}, nil
		},
	}
	h := newHandlerWithStubs(t, nil, android)
	// inventory has Raspberry as android in test helpers?
	r := dispatchJSON(t, h, "perf_fps", map[string]any{
		"device": "Raspberry", "package": "com.example.app", "window_sec": 2.0,
	})
	if r.IsError {
		// may fail resolve if Raspberry not android — check inventory
		body := resultText(t, &r)
		if strings.Contains(body, "unknown tool") {
			t.Fatal(body)
		}
		// try with raw serial pattern
		t.Logf("perf_fps body=%s", body)
	}
	// Use alias from newTestHandler inventory
	body := resultText(t, &r)
	if r.IsError {
		// If device not found as android, force via serial resolution
		// newHandlerWithStubs uses inventory with Raspberry android
		t.Fatalf("perf_fps error: %s", body)
	}
	if gotPkg != "com.example.app" {
		t.Errorf("pkg=%q", gotPkg)
	}
	if gotWin != 2*time.Second {
		t.Errorf("win=%v", gotWin)
	}
	if !strings.Contains(body, "60") && !strings.Contains(body, `"fps"`) {
		t.Errorf("body=%s", body)
	}
	_ = gotID
}

func TestPortForward_Lifecycle(t *testing.T) {
	var started, stopped bool
	android := &stubAdapter{
		forwardTCP: func(id string, localPort, devicePort int) (device.PortForward, error) {
			started = true
			return device.PortForward{LocalPort: 18080, DevicePort: devicePort, Spec: "tcp:18080 tcp:9000"}, nil
		},
		unforwardTCP: func(id string, localPort int) error {
			stopped = true
			if localPort != 18080 {
				t.Errorf("local=%d", localPort)
			}
			return nil
		},
		listForwards: func(id string) ([]device.PortForward, error) {
			return []device.PortForward{{LocalPort: 18080, DevicePort: 9000}}, nil
		},
	}
	h := newHandlerWithStubs(t, nil, android)
	r := dispatchJSON(t, h, "port_forward_start", map[string]any{
		"device": "Raspberry", "device_port": 9000.0, "local_port": 18080.0,
	})
	if r.IsError {
		t.Fatalf("start: %s", resultText(t, &r))
	}
	if !started {
		t.Error("forward not called")
	}
	r2 := dispatchJSON(t, h, "port_forward_list", map[string]any{"device": "Raspberry"})
	if r2.IsError {
		t.Fatalf("list: %s", resultText(t, &r2))
	}
	r3 := dispatchJSON(t, h, "port_forward_stop", map[string]any{
		"device": "Raspberry", "local_port": 18080.0,
	})
	if r3.IsError {
		t.Fatalf("stop: %s", resultText(t, &r3))
	}
	if !stopped {
		t.Error("unforward not called")
	}
}

func TestInputTapSwipe(t *testing.T) {
	var tapXY [2]int
	var swipe []int
	android := &stubAdapter{
		injectTap: func(id string, x, y int) error {
			tapXY = [2]int{x, y}
			return nil
		},
		injectSwipe: func(id string, x1, y1, x2, y2, durationMs int) error {
			swipe = []int{x1, y1, x2, y2, durationMs}
			return nil
		},
	}
	h := newHandlerWithStubs(t, nil, android)
	r := dispatchJSON(t, h, "input_tap", map[string]any{
		"device": "Raspberry", "x": 10.0, "y": 20.0,
	})
	if r.IsError {
		t.Fatalf("tap: %s", resultText(t, &r))
	}
	if tapXY != [2]int{10, 20} {
		t.Errorf("tap=%v", tapXY)
	}
	r2 := dispatchJSON(t, h, "input_swipe", map[string]any{
		"device": "Raspberry", "x1": 1.0, "y1": 2.0, "x2": 3.0, "y2": 4.0, "duration_ms": 50.0,
	})
	if r2.IsError {
		t.Fatalf("swipe: %s", resultText(t, &r2))
	}
	if len(swipe) != 5 || swipe[4] != 50 {
		t.Errorf("swipe=%v", swipe)
	}
}

func TestT111_IOSRejected(t *testing.T) {
	// iOS adapter without T111 interfaces
	ios := &stubAdapter{}
	h := newHandlerWithStubs(t, ios, nil)
	r := dispatchJSON(t, h, "input_tap", map[string]any{
		"device": "iPad", "x": 1.0, "y": 1.0,
	})
	if !r.IsError {
		t.Fatal("expected android-only error")
	}
	if !strings.Contains(resultText(t, &r), "Android") {
		t.Errorf("body=%s", resultText(t, &r))
	}
}
