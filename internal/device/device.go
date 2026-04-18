// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package device defines the cross-platform adapter interface for mobile
// devices. Concrete implementations shell out to platform-specific tooling
// (pymobiledevice3, devicectl, adb).
package device

// Info summarises a single connected device.
type Info struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Platform string `json:"platform"` // "ios" or "android"
	Model    string `json:"model,omitempty"`
	OS       string `json:"os,omitempty"`
	Alias    string `json:"alias,omitempty"` // populated from inventory
}

// State reports device runtime state. Fields are optional: nil pointers
// or empty strings indicate the field was unavailable (see Notes for why).
type State struct {
	BatteryLevel  *int     `json:"battery_level,omitempty"` // 0..100
	Charging      *bool    `json:"charging,omitempty"`
	ThermalState  string   `json:"thermal_state,omitempty"` // "nominal", "fair", "serious", "critical"
	ForegroundApp string   `json:"foreground_app,omitempty"`
	StorageFreeMB int64    `json:"storage_free_mb,omitempty"`
	Notes         []string `json:"notes,omitempty"` // degradation messages for unavailable fields
}

// AppInfo summarises an installed third-party application.
type AppInfo struct {
	BundleID string `json:"bundle_id"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
}

// Adapter is the platform-specific device surface.
type Adapter interface {
	// List returns all currently connected devices for this platform.
	List() ([]Info, error)

	// State reports runtime state for a device identified by its
	// platform-specific UUID (iOS) or serial (Android).
	State(id string) (State, error)

	// LaunchKeepAwake foregrounds the KeepAwake companion app on the
	// specified device so it holds the screen awake while plugged in.
	LaunchKeepAwake(id string) error

	// Screenshot captures the current screen as PNG bytes. iOS uses
	// pymobiledevice3 developer dvt (requires tunneld); Android uses
	// adb shell screencap.
	Screenshot(id string) ([]byte, error)

	// ListApps returns installed third-party apps.
	ListApps(id string) ([]AppInfo, error)

	// LaunchApp foregrounds an arbitrary app by bundle id. iOS needs
	// tunneld (dvt launch); Android uses adb monkey.
	LaunchApp(id, bundleID string) error

	// TerminateApp stops a running app by bundle id. iOS needs
	// tunneld (dvt process-id-for-bundle-id + kill); Android uses
	// adb am force-stop.
	TerminateApp(id, bundleID string) error
}
