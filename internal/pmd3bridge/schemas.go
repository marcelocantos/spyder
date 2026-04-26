// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

// DeviceInfo describes a connected iOS device as returned by /v1/list_devices.
type DeviceInfo struct {
	UDID        string `json:"udid"`
	Name        string `json:"name"`
	ProductType string `json:"product_type"`
	OSVersion   string `json:"os_version"`
}

// AppInfo describes an installed application as returned by /v1/list_apps.
type AppInfo struct {
	BundleID string  `json:"bundle_id"`
	Name     *string `json:"name,omitempty"`
	Version  *string `json:"version,omitempty"`
}

// Battery describes battery state as returned by /v1/battery.
type Battery struct {
	Level    *float64 `json:"level,omitempty"`
	Charging *bool    `json:"charging,omitempty"`
}

// CrashReport is one entry from /v1/crash_reports_list.
type CrashReport struct {
	Name      string `json:"name"`
	Process   string `json:"process"`
	Timestamp string `json:"timestamp"`
}

// --- request types (internal; used by Client methods) ---

type listDevicesRequest struct{}

type listAppsRequest struct {
	UDID string `json:"udid"`
}

type launchAppRequest struct {
	UDID     string `json:"udid"`
	BundleID string `json:"bundle_id"`
}

type launchAppResponse struct {
	PID int `json:"pid"`
}

type killAppRequest struct {
	UDID     string `json:"udid"`
	BundleID string `json:"bundle_id"`
}

type pidForBundleRequest struct {
	UDID     string `json:"udid"`
	BundleID string `json:"bundle_id"`
}

type pidForBundleResponse struct {
	PID *int `json:"pid"`
}

type batteryRequest struct {
	UDID string `json:"udid"`
}

type screenshotRequest struct {
	UDID string `json:"udid"`
}

type screenshotResponse struct {
	PNGBase64 string `json:"png_b64"`
}

type crashReportsListRequest struct {
	UDID     string  `json:"udid"`
	SinceISO *string `json:"since_iso8601,omitempty"`
	Process  *string `json:"process,omitempty"`
}

type crashReportsListResponse struct {
	Reports []CrashReport `json:"reports"`
}

type crashReportsPullRequest struct {
	UDID string `json:"udid"`
	Name string `json:"name"`
}

type crashReportsPullResponse struct {
	Content string `json:"content"`
}

type acquirePowerAssertionRequest struct {
	UDID       string  `json:"udid"`
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	TimeoutSec int     `json:"timeout_sec"`
	Details    *string `json:"details,omitempty"`
}

type acquirePowerAssertionResponse struct {
	HandleID string `json:"handle_id"`
}

type refreshPowerAssertionRequest struct {
	HandleID   string `json:"handle_id"`
	TimeoutSec int    `json:"timeout_sec"`
}

type releasePowerAssertionRequest struct {
	HandleID string `json:"handle_id"`
}

// DevicePowerState is the structured result from /v1/device_power_state (🎯T29).
//
// State values:
//
//	"awake"       — display on, DVT screenshot succeeded, non-trivial pixel content.
//	"display_off" — screenshot succeeded but framebuffer was all-black.
//	"asleep"      — screenshot failed with a pattern indicating device/display off.
//	"unknown"     — prerequisite missing (tunneld down, developer mode off) or
//	                unrecognised error; cannot determine state.
type DevicePowerState struct {
	State  string  `json:"state"`
	Detail *string `json:"detail,omitempty"`
}

type devicePowerStateRequest struct {
	UDID string `json:"udid"`
}

// listDevicesResponse wraps the devices array from the bridge.
type listDevicesResponse struct {
	Devices []DeviceInfo `json:"devices"`
}

// listAppsResponse wraps the apps array from the bridge.
type listAppsResponse struct {
	Apps []AppInfo `json:"apps"`
}
