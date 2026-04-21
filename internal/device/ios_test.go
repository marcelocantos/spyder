// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/pmd3bridge"
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

// --- IOSAdapter bridge-backed methods: fake bridge -------------------------

// fakeBridge implements a minimal subset of the pmd3bridge.Client API via a
// local interface so tests don't need to spin up a real HTTP server.
// IOSAdapter holds a *pmd3bridge.Client — we test the adapter against the
// real bridge type by providing pre-canned responses via a small HTTP test
// server, OR by extracting an interface for the adapter's needs.
//
// The approach here: we test error-classification logic by constructing
// BridgeErrors directly and verifying the adapter maps them to the right
// surface errors.

// TestIOSAdapter_NilBridge verifies that every bridge-dependent method on
// IOSAdapter returns errNoBridge when constructed without a bridge.
func TestIOSAdapter_NilBridge(t *testing.T) {
	a := NewIOSAdapter(nil)

	t.Run("State", func(t *testing.T) {
		_, err := a.State("UDID")
		if !errors.Is(err, errNoBridge) {
			t.Errorf("State err = %v; want errNoBridge", err)
		}
	})
	t.Run("Screenshot", func(t *testing.T) {
		_, err := a.Screenshot("UDID")
		if !errors.Is(err, errNoBridge) {
			t.Errorf("Screenshot err = %v; want errNoBridge", err)
		}
	})
	t.Run("ListApps", func(t *testing.T) {
		_, err := a.ListApps("UDID")
		if !errors.Is(err, errNoBridge) {
			t.Errorf("ListApps err = %v; want errNoBridge", err)
		}
	})
	t.Run("LaunchApp", func(t *testing.T) {
		err := a.LaunchApp("UDID", "com.example.app")
		if !errors.Is(err, errNoBridge) {
			t.Errorf("LaunchApp err = %v; want errNoBridge", err)
		}
	})
	t.Run("TerminateApp", func(t *testing.T) {
		err := a.TerminateApp("UDID", "com.example.app")
		if !errors.Is(err, errNoBridge) {
			t.Errorf("TerminateApp err = %v; want errNoBridge", err)
		}
	})
	t.Run("AppPID", func(t *testing.T) {
		_, err := a.AppPID("UDID", "com.example.app")
		if !errors.Is(err, errNoBridge) {
			t.Errorf("AppPID err = %v; want errNoBridge", err)
		}
	})
	t.Run("Crashes", func(t *testing.T) {
		_, err := a.Crashes("UDID", time.Time{}, "")
		if !errors.Is(err, errNoBridge) {
			t.Errorf("Crashes err = %v; want errNoBridge", err)
		}
	})
}

// TestIOSAdapter_EmptyID verifies that methods reject empty device IDs
// before touching the bridge (no bridge needed for this check).
func TestIOSAdapter_EmptyID(t *testing.T) {
	a := NewIOSAdapter(nil) // nil bridge — empty-ID check must fire first

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

// TestBridgeErrorClassification verifies that BridgeError codes round-trip
// through the pmd3bridge helper functions used by IOSAdapter.
func TestBridgeErrorClassification(t *testing.T) {
	paired := &pmd3bridge.BridgeError{Code: "device_not_paired", Status: 422}
	notInstalled := &pmd3bridge.BridgeError{Code: "bundle_not_installed", Status: 422}
	other := &pmd3bridge.BridgeError{Code: "internal_error", Status: 500}

	if !pmd3bridge.IsDeviceNotPaired(paired) {
		t.Error("IsDeviceNotPaired(paired) = false; want true")
	}
	if pmd3bridge.IsDeviceNotPaired(notInstalled) {
		t.Error("IsDeviceNotPaired(not_installed) = true; want false")
	}
	if pmd3bridge.IsBundleNotInstalled(notInstalled) == false {
		t.Error("IsBundleNotInstalled(not_installed) = false; want true")
	}
	if pmd3bridge.IsBundleNotInstalled(paired) {
		t.Error("IsBundleNotInstalled(paired) = true; want false")
	}
	if pmd3bridge.IsDeviceNotPaired(other) || pmd3bridge.IsBundleNotInstalled(other) {
		t.Error("other BridgeError should match neither classifier")
	}
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

// TestStateCache verifies that a cached State is returned without hitting the
// bridge (bridge is nil; a nil-bridge call would return errNoBridge, not a
// cached state, so we use a non-nil bridge that points to a non-existent
// socket and prime the cache manually).
func TestStateCache_ReturnsWithinTTL(t *testing.T) {
	a := NewIOSAdapter(pmd3bridge.NewClient("/nonexistent.sock"))
	// Prime the cache directly.
	chargingTrue := true
	primed := State{Charging: &chargingTrue}
	a.mu.Lock()
	a.cache["UDID"] = cachedState{state: primed, at: time.Now()}
	a.mu.Unlock()

	// Should get cache hit, no dial attempted.
	got, err := a.State("UDID")
	if err != nil {
		t.Fatalf("State err = %v; want nil (cache hit)", err)
	}
	if got.Charging == nil || !*got.Charging {
		t.Errorf("Charging = %v; want true (cached)", got.Charging)
	}
}

// TestStateCache_MissDialsBridge verifies that an expired cache entry causes
// the adapter to attempt to call the bridge. On failure the bridge error is
// captured in Notes rather than returned as an error (State is best-effort).
func TestStateCache_MissDialsBridge(t *testing.T) {
	a := NewIOSAdapter(pmd3bridge.NewClient("/nonexistent.sock"))
	// Prime with an expired entry.
	a.mu.Lock()
	a.cache["UDID"] = cachedState{state: State{}, at: time.Now().Add(-stateTTL - time.Second)}
	a.mu.Unlock()

	got, err := a.State("UDID")
	if err != nil {
		t.Fatalf("State err = %v; want nil (bridge errors go to Notes)", err)
	}
	// The battery call failed on the bad socket; Notes should capture the error.
	hasNote := false
	for _, n := range got.Notes {
		if n != "" {
			hasNote = true
			break
		}
	}
	if !hasNote {
		t.Error("expected at least one Note from bridge dial failure; got none")
	}
}
