// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"strings"
	"testing"
	"time"
)

// serial returns the live Android serial from the environment, or skips the
// test when unset. All live Android tests gate on SPYDER_LIVE_ANDROID_SERIAL.
func androidSerial(t *testing.T) string {
	t.Helper()
	s := os.Getenv("SPYDER_LIVE_ANDROID_SERIAL")
	if s == "" {
		t.Skip("SPYDER_LIVE_ANDROID_SERIAL not set; skipping live Android device test")
	}
	return s
}

// TestAndroidList_Live verifies that NewAndroidAdapter().List() returns at
// least one device and that the gated serial appears among them.
func TestAndroidList_Live(t *testing.T) {
	serial := androidSerial(t)

	adapter := NewAndroidAdapter()
	devs, err := adapter.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devs) == 0 {
		t.Fatalf("List returned 0 devices; expected at least the live device %s", serial)
	}
	t.Logf("List: %d device(s): %+v", len(devs), devs)

	found := false
	for _, d := range devs {
		if strings.Contains(d.UUID, serial) || d.UUID == serial {
			found = true
			t.Logf("live device entry: %+v", d)
		}
	}
	if !found {
		t.Errorf("live serial %s not found in List output: %+v", serial, devs)
	}
}

// TestAndroidState_Live verifies that State(serial) returns successfully and
// populates BatteryLevel.
func TestAndroidState_Live(t *testing.T) {
	serial := androidSerial(t)

	adapter := NewAndroidAdapter()
	state, err := adapter.State(serial)
	if err != nil {
		t.Fatalf("State(%s): %v", serial, err)
	}
	if state.BatteryLevel == nil {
		t.Errorf("State(%s).BatteryLevel is nil; expected a battery reading", serial)
	}
	t.Logf("State(%s): battery=%v charging=%v foreground=%q notes=%v",
		serial, ptrInt(state.BatteryLevel), ptrBool(state.Charging), state.ForegroundApp, state.Notes)
}

// TestAndroidListApps_Live verifies that ListApps(serial) returns at least one
// third-party app (pm list packages -3). Any normal Android device has user-
// installed apps.
func TestAndroidListApps_Live(t *testing.T) {
	serial := androidSerial(t)

	adapter := NewAndroidAdapter()
	apps, err := adapter.ListApps(serial)
	if err != nil {
		t.Fatalf("ListApps(%s): %v", serial, err)
	}
	if len(apps) == 0 {
		t.Fatalf("ListApps(%s) returned 0 apps; expected at least one user-installed app", serial)
	}
	n := min(5, len(apps))
	ids := make([]string, n)
	for i := range n {
		ids[i] = apps[i].BundleID
	}
	t.Logf("ListApps(%s): %d third-party apps; first %d: %v", serial, len(apps), n, ids)
}

// TestAndroidLogRange_Live drains logcat for up to 3 seconds and expects at
// least one structured line. Android devices always emit logcat traffic.
func TestAndroidLogRange_Live(t *testing.T) {
	serial := androidSerial(t)

	adapter := NewAndroidAdapter()
	lines, err := adapter.LogRange(serial, LogFilter{}, time.Time{}, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatalf("LogRange(%s): %v", serial, err)
	}
	if len(lines) == 0 {
		t.Errorf("LogRange(%s) returned 0 lines over ~3s; expected continuous logcat traffic", serial)
	}
	t.Logf("LogRange(%s): %d lines (first: %+v)", serial, len(lines), firstOrEmpty(lines))
}

// TestAndroidResolveExecutable_Live verifies that ResolveExecutable returns
// installed=true and executable==bundleID for com.android.settings, which is
// a system package present on every Android device.
func TestAndroidResolveExecutable_Live(t *testing.T) {
	serial := androidSerial(t)
	const pkg = "com.android.settings"

	adapter := NewAndroidAdapter()
	// com.android.settings is a system package; ListApps uses -3 (third-party
	// only). Use a user-visible package instead: if the device has any third-
	// party app, take the first one; otherwise fall back to a graceful skip.
	apps, err := adapter.ListApps(serial)
	if err != nil {
		t.Fatalf("ListApps(%s): %v", serial, err)
	}
	targetPkg := pkg
	if len(apps) > 0 {
		targetPkg = apps[0].BundleID
	} else {
		t.Skip("no third-party apps installed; cannot test ResolveExecutable")
	}

	exe, installed, err := adapter.ResolveExecutable(serial, targetPkg)
	if err != nil {
		t.Fatalf("ResolveExecutable(%s, %s): %v", serial, targetPkg, err)
	}
	if !installed {
		t.Errorf("ResolveExecutable(%s, %s): expected installed=true", serial, targetPkg)
	}
	if exe != targetPkg {
		t.Errorf("ResolveExecutable(%s, %s): executable=%q; want %q (identity on Android)", serial, targetPkg, exe, targetPkg)
	}
	t.Logf("ResolveExecutable(%s, %s): installed=%v executable=%q", serial, targetPkg, installed, exe)
}
