// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package device defines the cross-platform adapter interface for mobile
// devices. Concrete implementations shell out to platform-specific tooling
// (pymobiledevice3, devicectl, adb).
package device

import "time"

import "github.com/marcelocantos/spyder/internal/network"

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

// CrashReport summarises a single crash event. Path points to the local
// copy of the raw report (if pulled); Raw holds inline content when
// available. At least one of Path or Raw will typically be populated.
type CrashReport struct {
	Process   string    `json:"process"`
	Reason    string    `json:"reason,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Path      string    `json:"path,omitempty"` // local path to raw report on the host
	Raw       string    `json:"raw,omitempty"`  // inline raw content (optional)
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

	// Rotate sets the screen orientation of a simulator or emulator.
	// Supported orientations: portrait, landscape-left, landscape-right,
	// portrait-upside-down. Physical devices return a clear error.
	Rotate(id, orientation string) error

	// Crashes fetches crash reports from the device. since is the oldest
	// report to include (zero means all); process filters by process name
	// (empty means all). Reports are returned newest-first.
	//
	// iOS: pulls .ips files via pymobiledevice3 crash-reports. Each
	// report's first-line JSON header is parsed for structured metadata.
	//
	// Android: attempts tombstones via adb from /data/tombstones/
	// (root-capable devices only). Falls back to `adb logcat -b crash`
	// when tombstones are inaccessible.
	Crashes(id string, since time.Time, process string) ([]CrashReport, error)

	// StartRecording begins a screen recording to dest (an mp4 path).
	// Returns immediately; the recording runs asynchronously. The caller
	// must call StopRecording to finalise the file.
	//
	// iOS physical devices are not supported; they return an error immediately.
	// iOS simulators use `xcrun simctl io <udid> recordVideo <dest>`.
	// Android uses `adb shell screenrecord` and the file is pulled on stop.
	//
	// stopFn sends the termination signal to the subprocess. pid is the
	// subprocess PID, valid until StopRecording returns.
	StartRecording(id, dest string) (stopFn func() error, pid int, err error)

	// StopRecording signals the recorder subprocess to terminate cleanly
	// (SIGINT), waits for exit, and (for Android) pulls the output file
	// from the device to dest. pid is the value returned by StartRecording.
	StopRecording(id string, pid int) error

	// InstallApp installs an app on the device. path must point to
	// a .app or .ipa bundle (iOS) or a .apk file (Android). iOS uses
	// xcrun devicectl device install app; Android uses adb install -r.
	InstallApp(id, path string) error

	// UninstallApp removes an app by bundle id / package name. iOS uses
	// xcrun devicectl device uninstall app --bundle-identifier; Android
	// uses adb uninstall.
	UninstallApp(id, bundleID string) error

	// AppPID returns the process id of a running app identified by its
	// bundle id / package name, or an error if the app is not running.
	// iOS uses pymobiledevice3 developer dvt process-id-for-bundle-id
	// (requires tunneld); Android uses adb shell pidof.
	AppPID(id, bundleID string) (int, error)

	// ApplyNetwork shapes network conditions on the device according to
	// profile. Support varies by platform:
	//   - Android emulator: applied via "adb emu network speed/delay".
	//   - iOS simulator: not yet implemented; returns a clear error.
	//   - Physical devices (iOS or Android): not supported; returns a
	//     clear error explaining the limitation.
	ApplyNetwork(id string, profile network.NetworkProfile) error

	// ClearNetwork removes any previously applied network shaping and
	// restores full-speed connectivity. Support varies by platform in
	// the same way as ApplyNetwork.
	ClearNetwork(id string) error
}
