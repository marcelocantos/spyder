// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"strings"
	"testing"
	"time"
)

// enumerateDevices returns the list of paired iOS devices. If SPYDER_LIVE_UDIDS
// is set (comma-separated UDIDs) only those devices are returned; otherwise all
// devices returned by NewIOSAdapter().List() are used. The test is skipped (not
// failed) when no devices are found.
func enumerateDevices(t *testing.T) []string {
	t.Helper()
	if raw := os.Getenv("SPYDER_LIVE_UDIDS"); raw != "" {
		udids := strings.Split(raw, ",")
		var trimmed []string
		for _, u := range udids {
			if s := strings.TrimSpace(u); s != "" {
				trimmed = append(trimmed, s)
			}
		}
		if len(trimmed) > 0 {
			t.Logf("SPYDER_LIVE_UDIDS set; using %d explicit device(s)", len(trimmed))
			return trimmed
		}
	}

	adapter := NewIOSAdapter()
	devs, err := adapter.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devs) == 0 {
		t.Skip("no paired iOS devices found; skipping multi-device live test")
	}
	udids := make([]string, len(devs))
	for i, d := range devs {
		udids[i] = d.UUID
	}
	return udids
}

// TestMultiDevice_Enumerate_Live calls NewIOSAdapter().List(), asserts ≥1
// device is returned, and logs each entry. No env var required — the test
// exercises the enumeration path with whatever devices happen to be paired.
// It skips cleanly when no devices are found rather than failing, so it is
// safe to run on machines without physical iOS hardware.
func TestMultiDevice_Enumerate_Live(t *testing.T) {
	adapter := NewIOSAdapter()
	devs, err := adapter.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devs) == 0 {
		t.Log("WARNING: no paired iOS devices found; device stable appears empty")
		t.Skip("no paired iOS devices; skipping")
	}
	t.Logf("List: %d device(s)", len(devs))
	for _, d := range devs {
		t.Logf("  device: uuid=%s name=%q platform=%s model=%s os=%s", d.UUID, d.Name, d.Platform, d.Model, d.OS)
	}
}

// TestMultiDevice_LogRange_Live runs LogRange on each paired iOS device (or on
// those listed in SPYDER_LIVE_UDIDS) with a 2-second capture window and asserts
// that at least one device produced log output. A device that returns 0 lines is
// warned but does not individually fail the test — deep-idle devices may be
// quiet. The test fails only when every device returns 0 lines.
func TestMultiDevice_LogRange_Live(t *testing.T) {
	udids := enumerateDevices(t)
	adapter := NewIOSAdapter()

	totalLines := 0
	for _, udid := range udids {
		lines, err := adapter.LogRange(udid, LogFilter{}, time.Time{}, time.Now().Add(2*time.Second))
		if err != nil {
			t.Errorf("LogRange(%s): %v", udid, err)
			continue
		}
		t.Logf("LogRange(%s) over ~2s: %d lines (first: %+v)", udid, len(lines), firstOrEmpty(lines))
		if len(lines) == 0 {
			t.Logf("WARNING: LogRange(%s) returned 0 lines; device may be deep-idle", udid)
		}
		totalLines += len(lines)
	}

	if totalLines == 0 {
		t.Errorf("expected ≥1 syslog line across all %d device(s); got 0 total — log capture may be broken", len(udids))
	}
}

// TestMultiDevice_ResolveExecutable_Live calls ResolveExecutable for a
// well-known system bundle on each paired device, asserting the call
// itself returns no error. Uses com.apple.TestFlight since it's
// pre-installed on developer-paired devices.
func TestMultiDevice_ResolveExecutable_Live(t *testing.T) {
	const probeBundleID = "com.apple.TestFlight"

	udids := enumerateDevices(t)
	adapter := NewIOSAdapter()

	for _, udid := range udids {
		exe, installed, err := adapter.ResolveExecutable(udid, probeBundleID)
		if err != nil {
			t.Errorf("ResolveExecutable(%s, %s): %v", udid, probeBundleID, err)
			continue
		}
		t.Logf("ResolveExecutable(%s, %s): installed=%v executable=%q", udid, probeBundleID, installed, exe)
	}
}
