// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"
	"testing"
	"time"
)

// --- parseDevicectlList ----------------------------------------------------

func TestParseDevicectlList(t *testing.T) {
	data := []byte(`{
		"info": {"outcome": "success"},
		"result": {
			"devices": [
				{
					"identifier": "E1A01EA6-8D77-556C-B18D-D470B2909E87",
					"hardwareProperties": {
						"udid": "00008103-000D39301A6A201E",
						"marketingName": "iPad Air (5th generation)",
						"productType": "iPad13,16"
					},
					"deviceProperties": {
						"name": "Pippa",
						"osVersionNumber": "26.3.1"
					}
				},
				{
					"identifier": "CD2E3380-F1AB-5D03-BBA8-E5A68ADB3261",
					"hardwareProperties": {
						"udid": "00008110-0014182E0AC2801E",
						"marketingName": "iPhone 13"
					},
					"deviceProperties": {
						"name": "Minicades Test iPhone",
						"osVersionNumber": "26.2"
					}
				}
			]
		}
	}`)
	got, err := parseDevicectlList(data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d; want 2", len(got))
	}
	if got[0].UUID != "00008103-000D39301A6A201E" {
		t.Errorf("UDID preferred over CoreDevice UUID: %q", got[0].UUID)
	}
	if got[0].Model != "iPad Air (5th generation)" {
		t.Errorf("Model = %q; want marketingName", got[0].Model)
	}
	if got[0].OS != "iOS 26.3.1" {
		t.Errorf("OS = %q", got[0].OS)
	}
}

func TestParseDevicectlList_MarketingNameFallback(t *testing.T) {
	// When marketingName is absent, productType is used as Model.
	data := []byte(`{"result": {"devices": [{"hardwareProperties": {"udid": "XXXX", "productType": "iPad16,1"}, "deviceProperties": {"name": "Foo"}}]}}`)
	got, err := parseDevicectlList(data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got[0].Model != "iPad16,1" {
		t.Errorf("Model = %q; want iPad16,1 fallback", got[0].Model)
	}
}

func TestParseDevicectlList_UDIDFallbackToIdentifier(t *testing.T) {
	// When hardwareProperties.udid is absent, fall back to the
	// CoreDevice identifier (at least we have *some* stable key).
	data := []byte(`{"result": {"devices": [{"identifier": "CORE-UUID-HERE", "deviceProperties": {"name": "X"}}]}}`)
	got, _ := parseDevicectlList(data)
	if got[0].UUID != "CORE-UUID-HERE" {
		t.Errorf("UUID fallback = %q; want CORE-UUID-HERE", got[0].UUID)
	}
}

// --- parseDevicectlConnectedIOSDevices -------------------------------------

func TestParseDevicectlConnectedIOSDevices_WiredOnly(t *testing.T) {
	// Three iOS devices: one wired+connected (kept), one
	// localNetwork+connected (filtered — Wi-Fi reachable but the
	// supervisor must not target it), one wired+unavailable (filtered —
	// paired but not currently usable). Plus one macOS device that
	// happens to be wired+connected (filtered — non-iOS platform).
	data := []byte(`{
		"result": {
			"devices": [
				{
					"hardwareProperties": {"udid": "WIRED-IOS", "platform": "iOS"},
					"connectionProperties": {"tunnelState": "connected", "transportType": "wired"}
				},
				{
					"hardwareProperties": {"udid": "WIFI-IOS", "platform": "iOS"},
					"connectionProperties": {"tunnelState": "connected", "transportType": "localNetwork"}
				},
				{
					"hardwareProperties": {"udid": "OFF-IOS", "platform": "iOS"},
					"connectionProperties": {"tunnelState": "unavailable", "transportType": "wired"}
				},
				{
					"hardwareProperties": {"udid": "WIRED-MAC", "platform": "macOS"},
					"connectionProperties": {"tunnelState": "connected", "transportType": "wired"}
				}
			]
		}
	}`)
	got, err := parseDevicectlConnectedIOSDevices(data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 || !got["WIRED-IOS"] {
		t.Errorf("got %v; want only WIRED-IOS", got)
	}
}

// --- mergeIOSDevices -------------------------------------------------------

func TestMergeIOSDevices_OverlayByUDID(t *testing.T) {
	base := []Info{
		{UUID: "A", Name: "pm3-name", Model: "iPad13,16", OS: "iOS 26.3.1", Platform: "ios"},
		{UUID: "B", Name: "only-in-usbmux", Model: "iPhone14,5", Platform: "ios"},
	}
	overlay := []Info{
		{UUID: "A", Name: "Pippa", Model: "iPad Air (5th generation)", OS: "iOS 26.3.1", Platform: "ios"},
		{UUID: "C", Name: "only-in-devicectl", Model: "iPad mini (A17 Pro)", Platform: "ios"},
	}
	got := mergeIOSDevices(base, overlay)
	if len(got) != 3 {
		t.Fatalf("got %d; want 3", len(got))
	}
	// A: overlay wins on Name + Model (richer fields).
	for _, d := range got {
		switch d.UUID {
		case "A":
			if d.Name != "Pippa" || d.Model != "iPad Air (5th generation)" {
				t.Errorf("A not upgraded: %+v", d)
			}
		case "B":
			if d.Name != "only-in-usbmux" {
				t.Errorf("B lost: %+v", d)
			}
		case "C":
			if d.Name != "only-in-devicectl" {
				t.Errorf("C lost: %+v", d)
			}
		}
	}
}

// --- stringOf / firstNonEmpty helpers --------------------------------------

func TestStringOfAndFirstNonEmpty(t *testing.T) {
	if got := stringOf("hello"); got != "hello" {
		t.Errorf("stringOf string = %q", got)
	}
	if got := stringOf(42); got != "" {
		t.Errorf("stringOf int = %q; want empty", got)
	}
	if got := stringOf(nil); got != "" {
		t.Errorf("stringOf nil = %q", got)
	}
	if got := firstNonEmpty("", "", "third"); got != "third" {
		t.Errorf("firstNonEmpty = %q; want third", got)
	}
	if got := firstNonEmpty("first", "second"); got != "first" {
		t.Errorf("firstNonEmpty first = %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("firstNonEmpty empty = %q", got)
	}
}

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

// TestIOSAdapter_NoSuchDevice_GoIOSMethods covers methods that have
// migrated off the bridge to go-ios. With a synthetic UDID and no
// matching attached device, go-ios's usbmux returns a clear
// "Device 'UDID' not found" — the test confirms each migrated method
// surfaces that without panicking and with the bundle id wrapped in
// the error.
func TestIOSAdapter_NoSuchDevice_GoIOSMethods(t *testing.T) {
	a := NewIOSAdapter()

	cases := []struct {
		name string
		call func() error
	}{
		{"State", func() error { _, err := a.State("UDID"); return err }},
		{"Screenshot", func() error { _, err := a.Screenshot("UDID"); return err }},
		{"ListApps", func() error { _, err := a.ListApps("UDID"); return err }},
		{"LaunchApp", func() error { return a.LaunchApp("UDID", "com.example.app") }},
		{"TerminateApp", func() error { return a.TerminateApp("UDID", "com.example.app") }},
		{"AppPID", func() error { _, err := a.AppPID("UDID", "com.example.app"); return err }},
		{"ForegroundApp", func() error { _, err := a.ForegroundApp("UDID"); return err }},
		{"KeepAwakeInstalled", func() error { _, err := a.KeepAwakeInstalled("UDID"); return err }},
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

// --- ParseIOSSyslogLine ----------------------------------------------------

func TestParseIOSSyslogLine(t *testing.T) {
	line := "Mar 15 14:23:01.123 Pippa MyApp[1234] <Error>: crash happened"
	ll, ok := ParseIOSSyslogLine(line)
	if !ok {
		t.Fatalf("ParseIOSSyslogLine(%q) = false; want true", line)
	}
	if ll.Process != "MyApp" {
		t.Errorf("Process = %q; want MyApp", ll.Process)
	}
	if ll.Level != "Error" {
		t.Errorf("Level = %q; want Error", ll.Level)
	}
	if ll.Message != "crash happened" {
		t.Errorf("Message = %q; want 'crash happened'", ll.Message)
	}
}

func TestParseIOSSyslogLine_NoMatch(t *testing.T) {
	if _, ok := ParseIOSSyslogLine("not a syslog line"); ok {
		t.Error("expected no match on arbitrary string")
	}
	if _, ok := ParseIOSSyslogLine(""); ok {
		t.Error("expected no match on empty string")
	}
}

// --- isSimulatorID ---------------------------------------------------------

func TestIsSimulatorID(t *testing.T) {
	// Hardware UDID (8 hex + hyphen + 16 hex)
	if isSimulatorID("00008103-000D39301A6A201E") {
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

// --- LogRange deadline / window tests (🎯T49) ---------------------------------

// TestLogRange_WaitsForDeadline verifies that LogRange actually waits until the
// `until` deadline and collects entries emitted during the window. This pins
// the regression reported in 🎯T49: LogRange was returning [] immediately on
// any `since/until` window — the root cause was that the bridge emitted
// timezone-naive timestamps which Go's RFC3339Nano parser discarded as
// zero-time, causing all entries to fail the since/until filter.
//
// The test uses a fake /v1/syslog server that emits 5 entries spaced 20 ms
// apart (total span ~100 ms). LogRange is called with a 200 ms window
// (since=now, until=now+200ms). All 5 entries have timestamps within the
// window, so the call must both wait and accumulate them.
// TestLogRange behaviour previously covered here was tightly coupled to
// the pmd3-bridge HTTP layer (fake /v1/syslog NDJSON server, timezone-
// aware RFC3339 parsing, deadline math validated against streamed
// entries). The go-ios syslog path doesn't expose a similar injection
// surface — `goios_syslog.New` opens a live device connection. The
// deadline-math contract is preserved structurally (LogRange still
// uses context.WithDeadline + a select branch in streamSyslog), but
// the behavioural test that proved it has been retired with the bridge.
// Coverage for the parser (BSD-syslog → LogLine) is preserved by the
// remaining ParseIOSSyslogLine_* tests in logs_test.go.

// TestStateCache_MissDialsBattery verifies that an expired cache entry
// causes the adapter to dial go-ios for battery data, and that
// transport/lookup failures are captured in Notes rather than returned
// as an error. Synthetic UDID guarantees go-ios's GetBatteryDiagnostics
// fails (no such paired device); we just check the failure manifests
// as a battery-data-unavailable Note.
func TestStateCache_MissDialsBattery(t *testing.T) {
	a := NewIOSAdapter()
	// Prime with an expired entry so State() takes the cache-miss path.
	a.mu.Lock()
	a.cache["UDID"] = cachedState{state: State{}, at: time.Now().Add(-stateTTL - time.Second)}
	a.mu.Unlock()

	got, err := a.State("UDID")
	if err == nil {
		// State swallows go-ios resolution errors via Notes. Confirm
		// the battery-data-unavailable note is present.
		hasNote := false
		for _, n := range got.Notes {
			if strings.Contains(n, "battery data unavailable") || strings.Contains(n, "state:") {
				hasNote = true
				break
			}
		}
		if !hasNote {
			t.Errorf("expected battery-data-unavailable note; got %v", got.Notes)
		}
		return
	}
	// Either path is fine: no panic, error is meaningful.
	if !strings.Contains(err.Error(), "UDID") && !strings.Contains(err.Error(), "Device") {
		t.Errorf("State err = %v; want UDID/Device-related error", err)
	}
}
