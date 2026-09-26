// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T111 / 🎯T112 — shared OS control surface: frame stats, TCP port
// forward, and minimal tap/swipe inject. Android uses adb-backed OS
// paths; iOS uses usbmux forward (real) and fail-closed FPS/inject with
// pointers to cooperative tools. Callers must not shell out to adb or
// iproxy.

package mcp

import (
	"context"
	"fmt"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
)

// Optional capability interfaces — AndroidAdapter and IOSAdapter implement
// these (iOS fail-closes FPS/inject; both implement port forward).

type frameStatsMeasurer interface {
	MeasureFrameStats(ctx context.Context, id, packageName string, window time.Duration) (device.FrameStats, error)
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

type keyInjector interface {
	InjectKeyEvent(id string, keyCode int) error
}

type notificationShadeController interface {
	ExpandNotificationShade(id string) error
	CollapseNotificationShade(id string) error
	DisplaySize(id string) (int, int, error)
}

// androidKeyCodes maps friendly names to Android KEYCODE_* values.
var androidKeyCodes = map[string]int{
	"home":   3,
	"back":   4,
	"recent": 187, // APP_SWITCH
}

// requireOSControlCap authorizes and resolves a device for the shared OS
// control surface (Android + iOS). Desktop is rejected.
func (h *Handler) requireOSControlCap(dev, owner string) (device.Adapter, string, string, *mcpgo.CallToolResult) {
	if res := h.authorize(dev, owner); res != nil {
		return nil, "", "", res
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		res, _ := toolErr("%v", err)
		return nil, "", "", res
	}
	if platform != "android" && platform != "ios" {
		res, _ := toolErr("device %s is %s — OS control tools support android and ios only (🎯T111/T112)", dev, platform)
		return nil, "", "", res
	}
	return adapter, platform, id, nil
}

// requireAndroidCap is retained for any android-only call sites; prefer
// requireOSControlCap for the shared surface.
func (h *Handler) requireAndroidCap(dev, owner string) (device.Adapter, string, *mcpgo.CallToolResult) {
	ad, platform, id, res := h.requireOSControlCap(dev, owner)
	if res != nil {
		return nil, "", res
	}
	if platform != "android" {
		r, _ := toolErr("device %s is %s — this call requires android", dev, platform)
		return nil, "", r
	}
	return ad, id, nil
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
	// Max 120s must fit under DeadlinePerfFPS (150s = 120 + 30s margin).
	if windowSec > 120 {
		return toolErr("window_sec max is 120")
	}
	window := time.Duration(windowSec * float64(time.Second))
	// Local ctx so the wait is cancellable even though toolFunc has no Dispatch
	// context; bound matches DeadlinePerfFPS (window + margin).
	ctx, cancel := context.WithTimeout(context.Background(), window+DeadlinePerfFPSMargin)
	defer cancel()

	// Authorize + resolve under mu, but do not hold mu across the wait window.
	h.mu.Lock()
	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
	h.mu.Unlock()
	if errRes != nil {
		return errRes, nil
	}
	m, ok := adapter.(frameStatsMeasurer)
	if !ok {
		return toolErr("adapter does not support frame-stats measurement")
	}
	st, merr := m.MeasureFrameStats(ctx, id, pkg, window)
	if merr != nil {
		return toolErr("perf_fps: %v", merr)
	}
	note := "FPS = total_frames / window_sec from dumpsys gfxinfo (Android). For cooperative ge counters use app_perf_get / app_metrics_*."
	if platform == "ios" {
		note = "iOS has no compositor FPS path; this call should have failed closed — use app_perf_get / app_metrics_*."
	}
	return toolJSON(map[string]any{
		"device":   dev,
		"platform": platform,
		"result":   st,
		"note":     note,
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

	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
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
		"platform":    platform,
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

	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
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
		"platform":   platform,
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

	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
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
	return toolJSON(map[string]any{"device": dev, "platform": platform, "forwards": list})
}

// handleInputTap OS-level pixel tap (Android real; iOS fail-closed 🎯T112).
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

	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
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
	return toolJSON(map[string]any{"device": dev, "platform": platform, "x": x, "y": y, "injected": true})
}

// handleInputSwipe OS-level pixel swipe (Android real; iOS fail-closed 🎯T112).
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

	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
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
		"platform":    platform,
		"x1":          x1,
		"y1":          y1,
		"x2":          x2,
		"y2":          y2,
		"duration_ms": durationMs,
		"injected":    true,
	})
}

// handleInputKey sends an Android KEYCODE (home, back, recent). iOS fail-closed.
func (h *Handler) handleInputKey(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	keyName, err := requireString(args, "key")
	if err != nil {
		return nil, err
	}
	code, ok := androidKeyCodes[keyName]
	if !ok {
		return toolErr("input_key: unknown key %q — use home, back, or recent", keyName)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	inj, ok := adapter.(keyInjector)
	if !ok {
		return toolErr("input_key: not supported on %s (Android only)", platform)
	}
	if err := inj.InjectKeyEvent(id, code); err != nil {
		return toolErr("input_key: %v", err)
	}
	return toolJSON(map[string]any{"device": dev, "platform": platform, "key": keyName, "keycode": code, "injected": true})
}

// handleNotificationShade expands or collapses the Android notification shade.
func (h *Handler) handleNotificationShade(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	action := optString(args, "action")
	if action == "" {
		action = "expand"
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	ctrl, ok := adapter.(notificationShadeController)
	if !ok {
		return toolErr("notification_shade: not supported on %s (Android only)", platform)
	}
	switch action {
	case "expand":
		if err := ctrl.ExpandNotificationShade(id); err != nil {
			return toolErr("notification_shade: %v", err)
		}
	case "collapse":
		if err := ctrl.CollapseNotificationShade(id); err != nil {
			return toolErr("notification_shade: %v", err)
		}
	default:
		return toolErr("notification_shade: action must be expand or collapse")
	}
	return toolJSON(map[string]any{"device": dev, "platform": platform, "action": action})
}

// handleNotificationTapFirst expands the shade and taps the first notification
// row (heuristic — hybrid smoke when UI tree automation is unavailable).
func (h *Handler) handleNotificationTapFirst(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	yFrac := 0.15
	if v, ok := args["y_fraction"].(float64); ok && v > 0 && v < 1 {
		yFrac = v
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	ctrl, ok := adapter.(notificationShadeController)
	if !ok {
		return toolErr("notification_tap_first: not supported on %s (Android only)", platform)
	}
	inj, ok := adapter.(touchInjector)
	if !ok {
		return toolErr("notification_tap_first: adapter has no touch inject")
	}
	if err := ctrl.ExpandNotificationShade(id); err != nil {
		return toolErr("notification_tap_first: %v", err)
	}
	w, hgt, err := ctrl.DisplaySize(id)
	if err != nil {
		return toolErr("notification_tap_first: %v", err)
	}
	x := w / 2
	y := int(float64(hgt) * yFrac)
	if err := inj.InjectTap(id, x, y); err != nil {
		return toolErr("notification_tap_first: %v", err)
	}
	return toolJSON(map[string]any{
		"device": dev, "platform": platform, "x": x, "y": y, "width": w, "height": hgt,
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
