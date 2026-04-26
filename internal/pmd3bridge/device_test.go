// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build device

package pmd3bridge

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Device-tier tests (🎯T26.4) run against the real pmd3-bridge with
// real pymobiledevice3 against a real paired iOS device. Nothing is
// mocked. Tests skip if no device is present or pairing is not trusted.
//
// Gated behind the `device` build tag. Run via:
//
//   SPYDER_DEVICES=1 make test-report
//
// or directly:
//
//   go test -tags=device ./internal/pmd3bridge/...

// findDevBridgeForDevice reuses the same walk-up logic as the
// integration tier but intentionally duplicated so device_test.go and
// integration_test.go don't share state beyond the supervisor itself.
func findDevBridgeForDevice(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for range 8 {
		candidate := filepath.Join(dir, "scripts", "run-dev-bridge.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("scripts/run-dev-bridge.sh not found walking up from %s", thisFile)
	return ""
}

// startRealBridge spawns the bridge against real pmd3 (no fake
// services). Callers skip when no device is attached or the bridge
// can't enumerate one.
func startRealBridge(t *testing.T) (*Supervisor, *Client) {
	t.Helper()
	script := findDevBridgeForDevice(t)
	t.Setenv("SPYDER_LOG_LEVEL", "WARNING")

	sup := NewSupervisor(script,
		WithReadyTimeout(45*time.Second),
		WithShutdownTimeout(5*time.Second),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("bridge Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = sup.Stop(stopCtx)
	})
	return sup, sup.Client()
}

// firstIOSDevice returns an iOS device the bridge can see. If
// SPYDER_TEST_UDID is set, that exact UDID wins (so HIL runs can pin
// Pippa even when other devices are also tunneled). Otherwise, prefer
// devices with a populated lockdown name; fall back to the first
// device with a non-empty UDID — tunneld-only enrolment (iOS 17+ over
// WiFi) returns empty name fields from the bridge because devicectl
// backfill lives in the Go-side adapter, not in the bridge itself.
func firstIOSDevice(t *testing.T, c *Client) DeviceInfo {
	t.Helper()
	devs, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) == 0 {
		t.Skip("no iOS device attached; skipping device tier")
	}
	if pin := strings.TrimSpace(os.Getenv("SPYDER_TEST_UDID")); pin != "" {
		for _, d := range devs {
			if d.UDID == pin {
				return d
			}
		}
		t.Skipf("SPYDER_TEST_UDID=%q not present in %d attached devices; skipping device tier", pin, len(devs))
	}
	for _, d := range devs {
		if d.Name != "" && d.Name != "unknown" {
			return d
		}
	}
	for _, d := range devs {
		if d.UDID != "" {
			return d
		}
	}
	t.Skipf("no paired iOS device among %d attached; skipping device tier", len(devs))
	return DeviceInfo{}
}

func TestDevice_ListDevices_ReturnsSomething(t *testing.T) {
	_, c := startRealBridge(t)
	devs, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) == 0 {
		t.Skip("no iOS device attached; skipping device tier")
	}
	for _, d := range devs {
		t.Logf("device: udid=%s name=%q product=%s os=%s",
			d.UDID, d.Name, d.ProductType, d.OSVersion)
	}
}

func TestDevice_Battery(t *testing.T) {
	_, c := startRealBridge(t)
	d := firstIOSDevice(t, c)

	b, err := c.Battery(context.Background(), d.UDID)
	if err != nil {
		t.Fatalf("Battery(%s): %v", d.UDID, err)
	}
	if b.Level == nil {
		t.Error("battery level missing")
	} else if *b.Level < 0 || *b.Level > 1 {
		t.Errorf("battery level %v out of [0,1]", *b.Level)
	}
	t.Logf("battery: level=%v charging=%v", b.Level, b.Charging)
}

func TestDevice_Screenshot(t *testing.T) {
	_, c := startRealBridge(t)
	d := firstIOSDevice(t, c)

	png, err := c.Screenshot(context.Background(), d.UDID)
	if err != nil {
		t.Fatalf("Screenshot(%s): %v", d.UDID, err)
	}
	if len(png) < 64 {
		t.Errorf("screenshot too small: %d bytes", len(png))
	}
	if string(png[:4]) != "\x89PNG" {
		t.Errorf("not a PNG: magic=%q", png[:min(8, len(png))])
	}
	t.Logf("screenshot: %d bytes", len(png))
}

func TestDevice_PowerAssertionLifecycle(t *testing.T) {
	_, c := startRealBridge(t)
	d := firstIOSDevice(t, c)
	ctx := context.Background()

	// Acquire a short-lived assertion (30s), refresh once, release. This
	// exercises the real PowerAssertionManager against a real device.
	handle, err := c.AcquirePowerAssertion(ctx, d.UDID,
		"PreventUserIdleSystemSleep", "spyder device-tier test", 30, "")
	if err != nil {
		if strings.Contains(err.Error(), "not_paired") {
			t.Skipf("device %s not paired", d.UDID)
		}
		t.Fatalf("AcquirePowerAssertion: %v", err)
	}
	t.Logf("acquired handle=%s", handle)

	if err := c.RefreshPowerAssertion(ctx, handle, 30); err != nil {
		t.Errorf("RefreshPowerAssertion: %v", err)
	}
	if err := c.ReleasePowerAssertion(ctx, handle); err != nil {
		t.Errorf("ReleasePowerAssertion: %v", err)
	}
}
