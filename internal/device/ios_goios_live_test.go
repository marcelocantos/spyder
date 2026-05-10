// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"testing"
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
