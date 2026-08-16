// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T112 — iOS backends for the shared OS control surface.
// Port forward is real (ios_forward.go). FPS and OS HID inject fail
// closed with pointers to cooperative tools / mobile-mcp — never silent
// no-ops.

package device

import (
	"context"
	"fmt"
	"time"
)

// ErrIOSPerfFPSUnsupported is returned by MeasureFrameStats on iOS.
// Physical iOS has no free compositor FPS twin of dumpsys gfxinfo;
// agents should use app_perf_get / app_metrics_* when the app is on
// app-channel.
const ErrIOSPerfFPSUnsupported = "perf_fps is not supported on iOS (no compositor FPS path). " +
	"Use app_perf_get or app_metrics_* for cooperative ge counters, or measure on Android"

// ErrIOSInjectUnsupported is returned by InjectTap/InjectSwipe on iOS.
// Physical HID inject needs WDA/XCTest (mobile-mcp); cooperative apps
// should use app_input.
const ErrIOSInjectUnsupported = "OS input inject is not supported on iOS physical devices. " +
	"Use app_input (ge/app-channel) or mobile-mcp for UI automation"

// ErrIOSSettingUnsupported is returned by device_setting on iOS (🎯T112 / 🎯T130).
// There is no allowlisted system-settings path for refresh rate on iOS.
const ErrIOSSettingUnsupported = "device_setting is not supported on iOS (no allowlisted system-settings path). " +
	"Refresh rate and similar OS settings are Android-only"

// MeasureFrameStats implements the shared frameStats surface on iOS by
// failing closed with an actionable message (🎯T112).
func (a *IOSAdapter) MeasureFrameStats(ctx context.Context, id, packageName string, window time.Duration) (FrameStats, error) {
	if id == "" {
		return FrameStats{}, fmt.Errorf("device identifier is empty")
	}
	if packageName == "" {
		return FrameStats{}, fmt.Errorf("package is required")
	}
	if window <= 0 {
		return FrameStats{}, fmt.Errorf("window must be positive")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return FrameStats{}, err
		}
	}
	return FrameStats{}, fmt.Errorf("%s", ErrIOSPerfFPSUnsupported)
}

// InjectTap fails closed on iOS (🎯T112).
func (a *IOSAdapter) InjectTap(id string, x, y int) error {
	if id == "" {
		return fmt.Errorf("device identifier is empty")
	}
	if x < 0 || y < 0 {
		return fmt.Errorf("x and y must be non-negative pixel coordinates")
	}
	return fmt.Errorf("%s", ErrIOSInjectUnsupported)
}

// InjectSwipe fails closed on iOS (🎯T112).
func (a *IOSAdapter) InjectSwipe(id string, x1, y1, x2, y2, durationMs int) error {
	if id == "" {
		return fmt.Errorf("device identifier is empty")
	}
	if x1 < 0 || y1 < 0 || x2 < 0 || y2 < 0 {
		return fmt.Errorf("coordinates must be non-negative pixel values")
	}
	return fmt.Errorf("%s", ErrIOSInjectUnsupported)
}

// SetSystemSetting fails closed on iOS after allowlist check (🎯T130).
func (a *IOSAdapter) SetSystemSetting(id, key, value string) (SettingResult, error) {
	if err := iosSettingArgs(id, key); err != nil {
		return SettingResult{}, err
	}
	if value == "" {
		return SettingResult{}, fmt.Errorf("value is required to set %s", key)
	}
	return SettingResult{}, fmt.Errorf("%s", ErrIOSSettingUnsupported)
}

// RestoreSystemSetting fails closed on iOS after allowlist check (🎯T130).
func (a *IOSAdapter) RestoreSystemSetting(id, key string) (SettingResult, error) {
	if err := iosSettingArgs(id, key); err != nil {
		return SettingResult{}, err
	}
	return SettingResult{}, fmt.Errorf("%s", ErrIOSSettingUnsupported)
}

// GetSystemSetting fails closed on iOS after allowlist check (🎯T130).
func (a *IOSAdapter) GetSystemSetting(id, key string) (SettingResult, error) {
	if err := iosSettingArgs(id, key); err != nil {
		return SettingResult{}, err
	}
	return SettingResult{}, fmt.Errorf("%s", ErrIOSSettingUnsupported)
}

func iosSettingArgs(id, key string) error {
	if id == "" {
		return fmt.Errorf("device identifier is empty")
	}
	if _, err := SettingAndroidNames(key); err != nil {
		return err
	}
	return nil
}
