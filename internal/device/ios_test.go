// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"
	"testing"
	"time"
)

// Device enumeration parsing now lives in internal/devicectl
// (parseDevices, tested there with fixtures). IOSAdapter.List's
// devicectl-primary ordering and usbmux supplement/fallback are covered by
// TestIOSListDevicectlPrimary in ios_devicectl_test.go.

// --- truncate --------------------------------------------------------------

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 100); got != "hello" {
		t.Errorf("truncate short = %q; want hello", got)
	}
	got := truncate("hello world here", 5)
	if got != "hello…" {
		t.Errorf("truncate long = %q; want 'hello…'", got)
	}
	if got := truncate("   hello   ", 100); got != "hello" {
		t.Errorf("truncate strips whitespace = %q", got)
	}
}

// --- IOSAdapter ------------------------------------------------------------

// TestIOSAdapter_NoSuchDevice_GoIOSMethods covers the DTX/go-ios surface
// (Screenshot, Crashes). With a synthetic UDID and no tunnel, go-ios fails
// fast; the test confirms each surfaces the error without panicking. The
// devicectl-routed methods (ListApps/LaunchApp/TerminateApp/AppPID/State)
// are covered hermetically by ios_devicectl_test.go's stub-runner tests —
// they're excluded here because they would shell out to real `xcrun
// devicectl` against a bogus UDID.
func TestIOSAdapter_NoSuchDevice_GoIOSMethods(t *testing.T) {
	a := NewIOSAdapter()

	cases := []struct {
		name string
		call func() error
	}{
		{"Screenshot", func() error { _, err := a.Screenshot("UDID"); return err }},
		{"Crashes", func() error { _, err := a.Crashes("UDID", time.Time{}, ""); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("%s on synthetic UDID returned nil; expected device-not-found", tc.name)
			}
			// Any error message is acceptable; absence of panic is the
			// contract. Loose check that it's not just "<nil>".
			_ = err.Error()
		})
	}
}

// TestIOSAdapter_EmptyID verifies that methods reject empty device IDs
// before touching the bridge (no bridge needed for this check).
func TestIOSAdapter_EmptyID(t *testing.T) {
	a := NewIOSAdapter() // nil bridge — empty-ID check must fire first

	t.Run("State", func(t *testing.T) {
		_, err := a.State("")
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("State('') = %v; want 'empty' error", err)
		}
	})
	t.Run("Screenshot", func(t *testing.T) {
		_, err := a.Screenshot("")
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("Screenshot('') = %v; want 'empty' error", err)
		}
	})
	t.Run("ListApps", func(t *testing.T) {
		_, err := a.ListApps("")
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("ListApps('') = %v; want 'empty' error", err)
		}
	})
	t.Run("LaunchApp_emptyID", func(t *testing.T) {
		err := a.LaunchApp("", "com.example.app")
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("LaunchApp('','bundle') = %v; want 'required' error", err)
		}
	})
	t.Run("LaunchApp_emptyBundle", func(t *testing.T) {
		err := a.LaunchApp("UDID", "")
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("LaunchApp('UDID','') = %v; want 'required' error", err)
		}
	})
}

// --- isSimulatorID ---------------------------------------------------------

func TestIsSimulatorID(t *testing.T) {
	// Hardware UDID (8 hex + hyphen + 16 hex)
	if isSimulatorID("00008103-001122334455667A") {
		t.Error("hardware UDID mistakenly classified as simulator")
	}
	// Standard UUID (simulator / CoreDevice)
	if !isSimulatorID("C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8") {
		t.Error("simulator UUID not recognised")
	}
	// Bare string
	if !isSimulatorID("booted") {
		t.Error("'booted' should be treated as simulator ID")
	}
}

// --- IOSAdapter state cache ------------------------------------------------

// TestStateCache verifies that a cached State is returned without
// hitting go-ios. Primes the cache directly with a known value and
// confirms State() returns it.
func TestStateCache_ReturnsWithinTTL(t *testing.T) {
	a := NewIOSAdapter()
	chargingTrue := true
	primed := State{Charging: &chargingTrue}
	a.mu.Lock()
	a.cache["UDID"] = cachedState{state: primed, at: time.Now()}
	a.mu.Unlock()

	got, err := a.State("UDID")
	if err != nil {
		t.Fatalf("State err = %v; want nil (cache hit)", err)
	}
	if got.Charging == nil || !*got.Charging {
		t.Errorf("Charging = %v; want true (cached)", got.Charging)
	}
}

// LogRange's deadline-math contract (context.WithDeadline + the select
// branch in streamSyslog) is preserved structurally, but no in-process
// behavioural test exists: `goios_syslog.New` opens a live device
// connection and has no injection surface. Parser coverage (BSD-syslog
// → LogLine) is preserved by the ParseIOSSyslogLine_* tests in
// logs_test.go.

// State's cache-miss path with the lockdown (go-ios) path unavailable and
// the devicectl fallback succeeding/failing is covered hermetically by
// TestStateDevicectlFallback in ios_devicectl_test.go (a real go-ios dial
// against a synthetic UDID would either hang or shell out, so it's driven
// through a stub devicectl instead).
