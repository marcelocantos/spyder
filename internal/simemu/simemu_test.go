// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simemu

import (
	"encoding/json"
	"testing"
)

// --------------------------------------------------------------------------
// iOS simulator parser tests
// --------------------------------------------------------------------------

func TestParseSimDevicesJSON(t *testing.T) {
	raw := map[string]any{
		"devices": map[string]any{
			"com.apple.CoreSimulator.SimRuntime.iOS-17-5": []any{
				map[string]any{
					"udid":        "ABCD-1234",
					"name":        "iPhone 15",
					"state":       "Booted",
					"isAvailable": true,
				},
				map[string]any{
					"udid":        "EFGH-5678",
					"name":        "iPad Pro",
					"state":       "Shutdown",
					"isAvailable": true,
				},
			},
			"com.apple.CoreSimulator.SimRuntime.iOS-16-4": []any{
				map[string]any{
					"udid":        "IJKL-9012",
					"name":        "iPhone 14",
					"state":       "Shutdown",
					"isAvailable": false,
				},
			},
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	devices, err := ParseSimDevicesJSON(data)
	if err != nil {
		t.Fatalf("ParseSimDevicesJSON: %v", err)
	}
	if len(devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(devices))
	}

	byUDID := map[string]SimDevice{}
	for _, d := range devices {
		byUDID[d.UDID] = d
	}

	d := byUDID["ABCD-1234"]
	if d.Name != "iPhone 15" {
		t.Errorf("name: got %q, want %q", d.Name, "iPhone 15")
	}
	if d.State != "Booted" {
		t.Errorf("state: got %q, want %q", d.State, "Booted")
	}
	if d.RuntimeID != "com.apple.CoreSimulator.SimRuntime.iOS-17-5" {
		t.Errorf("runtimeID: got %q", d.RuntimeID)
	}

	if _, ok := byUDID["EFGH-5678"]; !ok {
		t.Error("iPad Pro entry missing")
	}
	if _, ok := byUDID["IJKL-9012"]; !ok {
		t.Error("iPhone 14 entry missing")
	}
}

func TestParseSimDevicesJSON_Empty(t *testing.T) {
	data := []byte(`{"devices":{}}`)
	devices, err := ParseSimDevicesJSON(data)
	if err != nil {
		t.Fatalf("ParseSimDevicesJSON: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("expected 0 devices, got %d", len(devices))
	}
}

func TestParseSimDeviceTypesJSON(t *testing.T) {
	raw := map[string]any{
		"deviceTypes": []any{
			map[string]any{
				"name":       "iPhone 15",
				"identifier": "com.apple.CoreSimulator.SimDeviceType.iPhone-15",
			},
			map[string]any{
				"name":       "iPad Air (5th generation)",
				"identifier": "com.apple.CoreSimulator.SimDeviceType.iPad-Air--5th-generation-",
			},
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	types, err := ParseSimDeviceTypesJSON(data)
	if err != nil {
		t.Fatalf("ParseSimDeviceTypesJSON: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(types))
	}
	if types[0].Name != "iPhone 15" {
		t.Errorf("name: got %q", types[0].Name)
	}
	if types[0].ID != "com.apple.CoreSimulator.SimDeviceType.iPhone-15" {
		t.Errorf("id: got %q", types[0].ID)
	}
}

func TestParseSimRuntimesJSON(t *testing.T) {
	raw := map[string]any{
		"runtimes": []any{
			map[string]any{
				"name":        "iOS 17.5",
				"identifier":  "com.apple.CoreSimulator.SimRuntime.iOS-17-5",
				"isAvailable": true,
			},
			map[string]any{
				"name":        "iOS 16.4",
				"identifier":  "com.apple.CoreSimulator.SimRuntime.iOS-16-4",
				"isAvailable": false,
			},
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	runtimes, err := ParseSimRuntimesJSON(data)
	if err != nil {
		t.Fatalf("ParseSimRuntimesJSON: %v", err)
	}
	if len(runtimes) != 2 {
		t.Fatalf("expected 2 runtimes, got %d", len(runtimes))
	}
	if runtimes[0].Name != "iOS 17.5" {
		t.Errorf("name: got %q", runtimes[0].Name)
	}
	if !runtimes[0].IsAvailable {
		t.Error("expected IsAvailable=true for iOS 17.5")
	}
	if runtimes[1].IsAvailable {
		t.Error("expected IsAvailable=false for iOS 16.4")
	}
}

// --------------------------------------------------------------------------
// Android AVD parser tests
// --------------------------------------------------------------------------

func TestParseAVDList(t *testing.T) {
	// Real-ish output from `avdmanager list avd`.
	output := `Available Android Virtual Devices:
    Name: Pixel_6_API_34
    Path: /Users/user/.android/avd/Pixel_6_API_34.avd
  Target: Google APIs (Google Inc.)
          Based on: Android 14.0 ("UpsideDownCake") Tag/ABI: google_apis/arm64-v8a
     ABI: google_apis/arm64-v8a
  Sdcard: 512M
---------
    Name: Nexus_5X_API_28
    Path: /Users/user/.android/avd/Nexus_5X_API_28.avd
  Target: Google Play (Google Inc.)
     ABI: x86_64
`

	avds, err := ParseAVDList(output)
	if err != nil {
		t.Fatalf("ParseAVDList: %v", err)
	}
	if len(avds) != 2 {
		t.Fatalf("expected 2 AVDs, got %d: %+v", len(avds), avds)
	}
	if avds[0].Name != "Pixel_6_API_34" {
		t.Errorf("name[0]: got %q", avds[0].Name)
	}
	if avds[0].ABI != "google_apis/arm64-v8a" {
		t.Errorf("abi[0]: got %q", avds[0].ABI)
	}
	if avds[1].Name != "Nexus_5X_API_28" {
		t.Errorf("name[1]: got %q", avds[1].Name)
	}
	if avds[1].ABI != "x86_64" {
		t.Errorf("abi[1]: got %q", avds[1].ABI)
	}
}

func TestParseAVDList_Empty(t *testing.T) {
	output := "Available Android Virtual Devices:\n"
	avds, err := ParseAVDList(output)
	if err != nil {
		t.Fatalf("ParseAVDList: %v", err)
	}
	if len(avds) != 0 {
		t.Errorf("expected 0 AVDs, got %d", len(avds))
	}
}

func TestParseAVDList_SingleEntry(t *testing.T) {
	output := `    Name: MyDevice
    Path: /home/user/.android/avd/MyDevice.avd
  Target: Android API 30
     ABI: arm64-v8a
`
	avds, err := ParseAVDList(output)
	if err != nil {
		t.Fatalf("ParseAVDList: %v", err)
	}
	if len(avds) != 1 {
		t.Fatalf("expected 1 AVD, got %d", len(avds))
	}
	if avds[0].Name != "MyDevice" {
		t.Errorf("name: got %q", avds[0].Name)
	}
	if avds[0].Path != "/home/user/.android/avd/MyDevice.avd" {
		t.Errorf("path: got %q", avds[0].Path)
	}
	if avds[0].Target != "Android API 30" {
		t.Errorf("target: got %q", avds[0].Target)
	}
	if avds[0].ABI != "arm64-v8a" {
		t.Errorf("abi: got %q", avds[0].ABI)
	}
}
