// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestScreenshot_Live exercises the new go-ios-backed screenshot path.
func TestScreenshot_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}

	adapter := NewIOSAdapter()
	png, err := adapter.Screenshot(udid)
	if err != nil {
		t.Fatalf("Screenshot(%s): %v", udid, err)
	}
	if len(png) < 1024 {
		t.Errorf("Screenshot(%s) returned %d bytes; expected a non-trivial PNG", udid, len(png))
	}
	if string(png[:4]) != "\x89PNG" {
		t.Errorf("Screenshot(%s) output doesn't start with PNG magic; first 8 bytes = %x", udid, png[:8])
	}
	t.Logf("Screenshot(%s): %d bytes, PNG magic ok", udid, len(png))
}

// TestListApps_Live exercises the new installationproxy.BrowseUserApps path.
func TestListApps_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}

	adapter := NewIOSAdapter()
	apps, err := adapter.ListApps(udid)
	if err != nil {
		t.Fatalf("ListApps(%s): %v", udid, err)
	}
	if len(apps) == 0 {
		t.Fatalf("ListApps(%s) returned 0 apps; device should have at least one user app", udid)
	}
	t.Logf("ListApps(%s): %d user apps; first 3: %+v", udid, len(apps), apps[:min(3, len(apps))])
}

// TestLogRange_Live exercises the new go-ios.syslog path. Drains live
// log lines for 3 seconds and expects at least one entry — iOS devices
// continuously emit syslog so an empty result is a problem.
func TestLogRange_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}
	adapter := NewIOSAdapter()
	// Ignore the time window — pass zero values so all entries pass the
	// since/until check. (Unrelated to deadline: there's a default 5s
	// cap when until is zero.)
	lines, err := adapter.LogRange(udid, LogFilter{}, time.Time{}, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatalf("LogRange: %v", err)
	}
	t.Logf("LogRange(%s) over ~3s: %d lines (first: %+v)", udid, len(lines), firstOrEmpty(lines))
	if len(lines) == 0 {
		t.Errorf("expected ≥1 syslog line over 3s on a live device; got 0")
	}
}

// TestLogRangeThirdPartyApp_Live is the headline-feature regression
// guard for 🎯T58.1: a client that has just called launch_app for a
// third-party app must be able to fetch that app's own log emissions
// via LogRange filtered by its executable name. This is the exact
// workflow the v0.36–v0.38 features were sold on.
//
// Bundle id resolution order:
//  1. SPYDER_LIVE_BUNDLE_ID env var (explicit caller override).
//  2. First third-party app from installation_proxy that isn't an
//     Apple-prefixed bundle. Lets the test run on any paired device
//     with at least one user app without per-device configuration.
//
// Skips (not fails) when no usable bundle is found — keeps the test
// useful on freshly-provisioned devices.
//
// Pre-T58.1, this test would have caught the empty-stream regression
// that motivated the whole DTX activitytracetap port — the lockdown
// `os_trace_relay` path used to return zero entries for any third-
// party process filter on iOS 17+.
func TestLogRangeThirdPartyApp_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}

	adapter := NewIOSAdapter()
	bundleID := os.Getenv("SPYDER_LIVE_BUNDLE_ID")
	var exe string
	if bundleID != "" {
		e, installed, err := adapter.ResolveExecutable(udid, bundleID)
		if err != nil {
			t.Fatalf("ResolveExecutable(%s): %v", bundleID, err)
		}
		if !installed {
			t.Skipf("bundle %s (from SPYDER_LIVE_BUNDLE_ID) not installed on %s; skipping", bundleID, udid)
		}
		exe = e
		// Cold-start the named app.
		_ = adapter.TerminateApp(udid, bundleID)
		time.Sleep(250 * time.Millisecond)
		if err := adapter.LaunchApp(udid, bundleID); err != nil {
			t.Fatalf("LaunchApp(%s): %v", bundleID, err)
		}
	} else {
		// Walk the app list, trying each candidate until one launches.
		// Some apps fail to start (provisioning expired, signing identity
		// gone) and surface as "pidFromResponse: could not get pid from
		// response" — go-ios's way of saying the launch RPC didn't get
		// a pid back. Skip those and try the next.
		apps, err := adapter.ListApps(udid)
		if err != nil {
			t.Fatalf("ListApps: %v", err)
		}
		for _, a := range apps {
			if a.BundleID == "" || a.Executable == "" {
				continue
			}
			if strings.HasPrefix(a.BundleID, "com.apple.") {
				continue
			}
			// Terminate first so the launch forces a cold start —
			// scene-phase / UIKit lifecycle entries on launch are the
			// most reliable signal a quiescent app produces.
			_ = adapter.TerminateApp(udid, a.BundleID)
			time.Sleep(250 * time.Millisecond)
			if launchErr := adapter.LaunchApp(udid, a.BundleID); launchErr != nil {
				t.Logf("candidate %s did not launch: %v; trying next", a.BundleID, launchErr)
				continue
			}
			bundleID = a.BundleID
			exe = a.Executable
			break
		}
		if bundleID == "" {
			t.Skipf("no third-party app on %s could be cold-started; skipping (set SPYDER_LIVE_BUNDLE_ID to override)", udid)
		}
		t.Logf("auto-selected and launched test bundle: %s (exe=%s)", bundleID, exe)
	}

	// 5s window — long enough to capture UIKit/SwiftUI lifecycle
	// entries that fire on launch.
	lines, err := adapter.LogRange(udid,
		LogFilter{Process: exe},
		time.Time{}, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatalf("LogRange filtered by %s (exe=%s): %v", bundleID, exe, err)
	}
	if len(lines) == 0 {
		t.Errorf("expected >=1 log line emitted by %s (exe=%s) within 5s of launch; got 0 — third-party app log capture is broken", bundleID, exe)
	} else {
		t.Logf("LogRange(%s, process=%s): %d lines (first: %+v)", udid, exe, len(lines), lines[0])
	}
}

func firstOrEmpty(lines []LogLine) any {
	if len(lines) == 0 {
		return "<no lines>"
	}
	return lines[0]
}

// TestList_Live exercises the new go-ios.ListDevices + lockdown
// enrichment path. With at least one paired device attached, expects
// at least one entry in the returned list.
func TestList_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}
	adapter := NewIOSAdapter()
	devs, err := adapter.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devs) == 0 {
		t.Fatalf("List returned 0 devices; expected the live device %s", udid)
	}
	found := false
	for _, d := range devs {
		if d.UUID == udid {
			found = true
			t.Logf("live device entry: %+v", d)
		}
	}
	if !found {
		t.Errorf("live device %s missing from List output: got %v", udid, devs)
	}
}

// TestState_Live exercises the new go-ios lockdown battery path.
func TestState_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}

	adapter := NewIOSAdapter()
	state, err := adapter.State(udid)
	if err != nil {
		t.Fatalf("State(%s): %v", udid, err)
	}
	t.Logf("State(%s): battery=%v charging=%v notes=%v",
		udid, ptrInt(state.BatteryLevel), ptrBool(state.Charging), state.Notes)
}

func ptrInt(p *int) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func ptrBool(p *bool) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}
