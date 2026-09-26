// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T111 — minimal OS-level tap/swipe via `adb shell input`.
// Not full UI automation (no tree/OCR); that remains mobile-mcp.

package device

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// InjectTap injects a tap at pixel coordinates (device display pixels).
func (a *AndroidAdapter) InjectTap(id string, x, y int) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if x < 0 || y < 0 {
		return errors.New("x and y must be non-negative pixel coordinates")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	_, stderr, err := androidAdb("-s", id, "shell", "input", "tap",
		strconv.Itoa(x), strconv.Itoa(y))
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return fmt.Errorf("device not connected: %s", id)
		}
		return fmt.Errorf("adb input tap: %v\n%s", err, truncate(msg, 200))
	}
	return nil
}

// InjectSwipe injects a swipe from (x1,y1) to (x2,y2) lasting durationMs
// (Android `input swipe` duration; default 300 when durationMs <= 0).
func (a *AndroidAdapter) InjectSwipe(id string, x1, y1, x2, y2, durationMs int) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if x1 < 0 || y1 < 0 || x2 < 0 || y2 < 0 {
		return errors.New("coordinates must be non-negative pixel values")
	}
	if durationMs <= 0 {
		durationMs = 300
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	_, stderr, err := androidAdb("-s", id, "shell", "input", "swipe",
		strconv.Itoa(x1), strconv.Itoa(y1),
		strconv.Itoa(x2), strconv.Itoa(y2),
		strconv.Itoa(durationMs))
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return fmt.Errorf("device not connected: %s", id)
		}
		return fmt.Errorf("adb input swipe: %v\n%s", err, truncate(msg, 200))
	}
	return nil
}

// InjectKeyEvent sends a key event via `adb shell input keyevent`.
func (a *AndroidAdapter) InjectKeyEvent(id string, keyCode int) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if keyCode <= 0 {
		return errors.New("keyCode must be positive")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	_, stderr, err := androidAdb("-s", id, "shell", "input", "keyevent",
		strconv.Itoa(keyCode))
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return fmt.Errorf("device not connected: %s", id)
		}
		return fmt.Errorf("adb input keyevent: %v\n%s", err, truncate(msg, 200))
	}
	return nil
}

// ExpandNotificationShade opens the notification shade (Android 7+).
func (a *AndroidAdapter) ExpandNotificationShade(id string) error {
	return a.runStatusBarCmd(id, "expand-notifications")
}

// CollapseNotificationShade closes the status bar / shade.
func (a *AndroidAdapter) CollapseNotificationShade(id string) error {
	return a.runStatusBarCmd(id, "collapse")
}

func (a *AndroidAdapter) runStatusBarCmd(id string, subcmd string) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	_, stderr, err := androidAdb("-s", id, "shell", "cmd", "statusbar", subcmd)
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return fmt.Errorf("device not connected: %s", id)
		}
		return fmt.Errorf("adb statusbar %s: %v\n%s", subcmd, err, truncate(msg, 200))
	}
	return nil
}

// DisplaySize returns the logical display size in pixels (width, height).
func (a *AndroidAdapter) DisplaySize(id string) (int, int, error) {
	if id == "" {
		return 0, 0, errors.New("device identifier is empty")
	}
	stdout, stderr, err := androidAdb("-s", id, "shell", "wm", "size")
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return 0, 0, fmt.Errorf("device not connected: %s", id)
		}
		return 0, 0, fmt.Errorf("adb wm size: %v\n%s", err, truncate(msg, 200))
	}
	// Physical size: 1080x2340
	line := strings.TrimSpace(string(stdout))
	const prefix = "Physical size: "
	if strings.Contains(line, prefix) {
		line = strings.TrimPrefix(line, prefix)
	}
	parts := strings.Split(line, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected wm size output: %q", string(stdout))
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("parse wm size: %q", line)
	}
	return w, h, nil
}
