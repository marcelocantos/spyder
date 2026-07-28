// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T111 — OS-level Android control primitives: frame stats (gfxinfo FPS
// window), TCP port forward, and minimal tap/swipe inject. Callers must
// not shell out to adb; spyder owns the invocation.

package mcp

import (
	"fmt"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
)

// Optional capability interfaces — AndroidAdapter implements these;
// other platforms fail with a clear "not supported" message.

type frameStatsMeasurer interface {
	MeasureFrameStats(id, packageName string, window time.Duration) (device.FrameStats, error)
}

type portForwarder interface {
	ForwardTCP(id string, localPort, devicePort int) (device.PortForward, error)
	UnforwardTCP(id string, localPort int) error
	ListForwards(id string) ([]device.PortForward, error)
}

type touchInjector interface {
	InjectTap(id string, x, y int) error
	InjectSwipe(id string, x1, y1, x2, y2, durationMs int) error
}

func (h *Handler) requireAndroidCap(dev, owner string) (device.Adapter, string, *mcpgo.CallToolResult) {
	if res := h.authorize(dev, owner); res != nil {
		return nil, "", res
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		res, _ := toolErr("%v", err)
		return nil, "", res
	}
	if platform != "android" {
		res, _ := toolErr("device %s is %s — this tool is Android-only (🎯T111)", dev, platform)
		return nil, "", res
	}
	return adapter, id, nil
}

// handlePerfFPS measures compositor/UI FPS over a window via gfxinfo.
func (h *Handler) handlePerfFPS(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	pkg := optString(args, "package")
	if pkg == "" {
		pkg = optString(args, "bundle_id")
	}
	if pkg == "" {
		return toolErr("package (or bundle_id) is required")
	}
	owner := optString(args, "owner")
	windowSec := 3.0
	if v, ok := args["window_sec"].(float64); ok && v > 0 {
		windowSec = v
	}
	if windowSec > 120 {
		return toolErr("window_sec max is 120")
	}

	// Authorize + resolve under mu, but do not hold mu across the wait window.
	h.mu.Lock()
	adapter, id, errRes := h.requireAndroidCap(dev, owner)
	h.mu.Unlock()
	if errRes != nil {
		return errRes, nil
	}
	m, ok := adapter.(frameStatsMeasurer)
	if !ok {
		return toolErr("adapter does not support frame-stats measurement")
	}
	st, merr := m.MeasureFrameStats(id, pkg, time.Duration(windowSec*float64(time.Second)))
	if merr != nil {
		return toolErr("perf_fps: %v", merr)
	}
	return toolJSON(map[string]any{
		"device": dev,
		"result": st,
		"note":   "FPS = total_frames / window_sec from dumpsys gfxinfo (compositor/UI path). For cooperative ge counters use app_perf_get / app_metrics_*.",
	})
}

// handlePortForwardStart installs an adb TCP forward.
func (h *Handler) handlePortForwardStart(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	devicePort, err := requireInt(args, "device_port")
	if err != nil {
		return nil, err
	}
	localPort := 0
	if v, ok := args["local_port"].(float64); ok {
		localPort = int(v)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, id, errRes := h.requireAndroidCap(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	pf, ok := adapter.(portForwarder)
	if !ok {
		return toolErr("adapter does not support port forward")
	}
	fw, ferr := pf.ForwardTCP(id, localPort, devicePort)
	if ferr != nil {
		return toolErr("port_forward_start: %v", ferr)
	}
	return toolJSON(map[string]any{
		"device":      dev,
		"local_port":  fw.LocalPort,
		"device_port": fw.DevicePort,
		"spec":        fw.Spec,
		"host_url":    fmt.Sprintf("127.0.0.1:%d", fw.LocalPort),
	})
}

func (h *Handler) handlePortForwardStop(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	localPort, err := requireInt(args, "local_port")
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, id, errRes := h.requireAndroidCap(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	pf, ok := adapter.(portForwarder)
	if !ok {
		return toolErr("adapter does not support port forward")
	}
	if err := pf.UnforwardTCP(id, localPort); err != nil {
		return toolErr("port_forward_stop: %v", err)
	}
	return toolJSON(map[string]any{
		"device":     dev,
		"local_port": localPort,
		"removed":    true,
	})
}

func (h *Handler) handlePortForwardList(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	// list is observational — still authorize for consistency when owner set
	owner := optString(args, "owner")

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, id, errRes := h.requireAndroidCap(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	pf, ok := adapter.(portForwarder)
	if !ok {
		return toolErr("adapter does not support port forward")
	}
	list, lerr := pf.ListForwards(id)
	if lerr != nil {
		return toolErr("port_forward_list: %v", lerr)
	}
	return toolJSON(map[string]any{"device": dev, "forwards": list})
}

// handleInputTap OS-level pixel tap (Android).
func (h *Handler) handleInputTap(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	x, err := requireInt(args, "x")
	if err != nil {
		return nil, err
	}
	y, err := requireInt(args, "y")
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, id, errRes := h.requireAndroidCap(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	inj, ok := adapter.(touchInjector)
	if !ok {
		return toolErr("adapter does not support OS input inject")
	}
	if err := inj.InjectTap(id, x, y); err != nil {
		return toolErr("input_tap: %v", err)
	}
	return toolJSON(map[string]any{"device": dev, "x": x, "y": y, "injected": true})
}

// handleInputSwipe OS-level pixel swipe (Android).
func (h *Handler) handleInputSwipe(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	x1, err := requireInt(args, "x1")
	if err != nil {
		return nil, err
	}
	y1, err := requireInt(args, "y1")
	if err != nil {
		return nil, err
	}
	x2, err := requireInt(args, "x2")
	if err != nil {
		return nil, err
	}
	y2, err := requireInt(args, "y2")
	if err != nil {
		return nil, err
	}
	durationMs := 300
	if v, ok := args["duration_ms"].(float64); ok && v > 0 {
		durationMs = int(v)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, id, errRes := h.requireAndroidCap(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	inj, ok := adapter.(touchInjector)
	if !ok {
		return toolErr("adapter does not support OS input inject")
	}
	if err := inj.InjectSwipe(id, x1, y1, x2, y2, durationMs); err != nil {
		return toolErr("input_swipe: %v", err)
	}
	return toolJSON(map[string]any{
		"device":      dev,
		"x1":          x1,
		"y1":          y1,
		"x2":          x2,
		"y2":          y2,
		"duration_ms": durationMs,
		"injected":    true,
	})
}

func requireInt(args map[string]any, key string) (int, error) {
	v, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	switch n := v.(type) {
	case float64:
		return int(n), nil
	case int:
		return n, nil
	case int64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("%s must be a number", key)
	}
}
