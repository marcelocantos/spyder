// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"testing"
	"time"
)

// TestForegroundApp_Live exercises the new go-ios-backed ForegroundApp
// against a real device. Gated by SPYDER_LIVE_UDID — when unset the test
// is skipped, so it doesn't run in routine `go test ./...` invocations
// or on machines without a paired device. When SPYDER_LIVE_UDID is set
// (e.g. a paired iPhone's UDID) and `ios tunnel start --userspace` is
// running, the test confirms the foreground-app probe round-trips
// successfully and returns a non-error result. The actual returned
// bundle ID is logged for visual inspection.
func TestForegroundApp_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}

	adapter := NewIOSAdapter(nil) // nil bridge — ForegroundApp is fully on go-ios now
	fg, err := adapter.ForegroundApp(udid)
	if err != nil {
		t.Fatalf("ForegroundApp(%s): %v", udid, err)
	}
	t.Logf("foreground on %s: %q", udid, fg)
}

// TestKeepAwakeInspect_Live exercises the new go-ios-backed
// installationproxy path. Same gating as TestForegroundApp_Live.
func TestKeepAwakeInspect_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}

	adapter := NewIOSAdapter(nil)
	installed, err := adapter.KeepAwakeInstalled(udid)
	if err != nil {
		t.Fatalf("KeepAwakeInstalled(%s): %v", udid, err)
	}
	version, err := adapter.KeepAwakeInstalledVersion(udid)
	if err != nil {
		t.Fatalf("KeepAwakeInstalledVersion(%s): %v", udid, err)
	}
	t.Logf("KeepAwake on %s: installed=%v version=%q", udid, installed, version)
}

// TestScreenshot_Live exercises the new go-ios-backed screenshot path.
func TestScreenshot_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live device test")
	}

	adapter := NewIOSAdapter(nil)
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

	adapter := NewIOSAdapter(nil)
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
	adapter := NewIOSAdapter(nil)
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
	adapter := NewIOSAdapter(nil)
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

	adapter := NewIOSAdapter(nil)
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

