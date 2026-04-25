// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build device

package pmd3bridge

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// 🎯T30 acceptance test. Compares the bridge's screenshot endpoint
// against the ground truth from `xcrun devicectl list devices` —
// Apple's CoreDevice CLI. Asserts a real PNG comes back for every iOS
// 17+ device CoreDevice reports as connected.
//
// Pre-fix (legacy ScreenshotService → com.apple.mobile.screenshotr):
// fails with InvalidServiceError on every iOS 17+ device.
//
// Post-fix (DVT-based Screenshot instrument over tunneld-mediated RSD):
// passes for every connected iOS 17+ device.

type devicectlConnectedDevice struct {
	UDID      string
	Name      string
	OSVersion string
}

// devicectlConnectedIOSDevices returns the iOS devices CoreDevice
// reports as currently `connected`. Skips the test when devicectl is
// absent (non-macOS host) or no connected iOS devices are present.
func devicectlConnectedIOSDevices(t *testing.T) []devicectlConnectedDevice {
	t.Helper()
	if _, err := exec.LookPath("xcrun"); err != nil {
		t.Skip("xcrun not found; ground truth requires macOS + Xcode")
	}
	out, err := exec.Command("xcrun", "devicectl", "list", "devices",
		"--json-output", "-").Output()
	if err != nil {
		t.Fatalf("xcrun devicectl: %v", err)
	}

	var raw struct {
		Result struct {
			Devices []struct {
				DeviceProperties struct {
					Name      string `json:"name"`
					OSVersion string `json:"osVersionNumber"`
				} `json:"deviceProperties"`
				HardwareProperties struct {
					UDID     string `json:"udid"`
					Platform string `json:"platform"`
				} `json:"hardwareProperties"`
				ConnectionProperties struct {
					TunnelState string `json:"tunnelState"`
				} `json:"connectionProperties"`
			} `json:"devices"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("decode devicectl output: %v", err)
	}

	var devs []devicectlConnectedDevice
	for _, d := range raw.Result.Devices {
		if d.HardwareProperties.Platform != "iOS" {
			continue
		}
		if d.ConnectionProperties.TunnelState != "connected" {
			continue
		}
		devs = append(devs, devicectlConnectedDevice{
			UDID:      d.HardwareProperties.UDID,
			Name:      d.DeviceProperties.Name,
			OSVersion: d.DeviceProperties.OSVersion,
		})
	}
	return devs
}

// majorOSVersion parses the leading integer of an iOS version string
// (e.g. "26.3.1 (a)" → 26, "17.0" → 17). Returns 0 for an unparseable
// string so callers can decide whether to filter or include.
func majorOSVersion(s string) int {
	if i := strings.IndexByte(s, '.'); i > 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, ' '); i > 0 {
		s = s[:i]
	}
	n, _ := strconv.Atoi(s)
	return n
}

// TestDevice_Screenshot_WorksOniOS17Plus asserts the bridge's
// /v1/screenshot endpoint returns a valid PNG for every iOS 17+ device
// CoreDevice reports as connected. This is 🎯T30's named acceptance
// test.
func TestDevice_Screenshot_WorksOniOS17Plus(t *testing.T) {
	groundTruth := devicectlConnectedIOSDevices(t)
	if len(groundTruth) == 0 {
		t.Skip("no connected iOS devices; cannot exercise T30 screenshot regression")
	}

	var ios17Plus []devicectlConnectedDevice
	for _, gt := range groundTruth {
		if majorOSVersion(gt.OSVersion) >= 17 {
			ios17Plus = append(ios17Plus, gt)
		}
	}
	if len(ios17Plus) == 0 {
		t.Skip("no iOS 17+ devices connected; cannot exercise T30 screenshot regression")
	}

	_, c := startRealBridge(t)

	for _, gt := range ios17Plus {
		t.Run(gt.Name, func(t *testing.T) {
			png, err := c.Screenshot(context.Background(), gt.UDID)
			if err != nil {
				t.Fatalf("Screenshot(%s, iOS %s): %v", gt.Name, gt.OSVersion, err)
			}
			if len(png) < 4096 {
				t.Errorf("screenshot suspiciously small: %d bytes (expected > 4 KB)", len(png))
			}
			if string(png[:4]) != "\x89PNG" {
				t.Errorf("not a PNG: magic=%q", png[:min(8, len(png))])
			}
			t.Logf("device=%s ios=%s bytes=%d", gt.Name, gt.OSVersion, len(png))
		})
	}
}
