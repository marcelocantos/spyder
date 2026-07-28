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
