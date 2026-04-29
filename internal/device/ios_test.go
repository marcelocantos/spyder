// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/pmd3bridge"
)

// newTestServer stands up an httptest.Server on loopback TCP and returns
// the base URL. Under 🎯T26.1 the bridge is TCP-only.
func newTestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

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
	a := NewIOSAdapter(pmd3bridge.NewClient("http://127.0.0.1:1", "test-token"))
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
func TestLogRange_WaitsForDeadline(t *testing.T) {
	now := time.Now()
	const (
		entryCount     = 5
		entrySpacing   = 20 * time.Millisecond
		windowDuration = 200 * time.Millisecond
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/syslog", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher, canFlush := w.(http.Flusher)
		for i := 0; i < entryCount; i++ {
			// Emit an entry with a timestamp squarely inside the window.
			ts := now.Add(time.Duration(i+1) * entrySpacing)
			entry := map[string]any{
				"pid":       i + 1,
				"timestamp": ts.Format(time.RFC3339Nano),
				"level":     "INFO",
				"process":   "TestApp",
				"message":   "log line",
			}
			b, _ := json.Marshal(entry)
			_, _ = w.Write(b)
			_, _ = w.Write([]byte{'\n'})
			if canFlush {
				flusher.Flush()
			}
			time.Sleep(entrySpacing)
		}
		// Close body — the Go client's context deadline fires first or we
		// exhaust entries here; either way the call ends cleanly.
	})

	baseURL := newTestServer(t, mux)
	a := NewIOSAdapter(pmd3bridge.NewClient(baseURL, "test-token"))

	since := now
	until := now.Add(windowDuration)
	started := time.Now()
	lines, err := a.LogRange("UDID", LogFilter{}, since, until)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("LogRange returned error: %v", err)
	}

	// Must have waited at least half the window — proves the deadline math
	// is not returning immediately.
	const minWait = windowDuration / 2
	if elapsed < minWait {
		t.Errorf("LogRange returned too quickly (elapsed=%v; want >=%v) — deadline math is wrong", elapsed, minWait)
	}

	// Must have captured entries — proves the since/until filter and
	// timestamp parsing are working (timezone-aware RFC3339Nano shapes).
	if len(lines) == 0 {
		t.Error("LogRange returned [] — since/until filter dropped all entries (timezone or parse bug?)")
	}
}

// TestLogRange_PastWindowReturnsQuickly verifies that when `until` is already
// in the past, LogRange does not hang — the deadline fires immediately and the
// call returns.
func TestLogRange_PastWindowReturnsQuickly(t *testing.T) {
	// Server that blocks forever (simulates a live stream).
	released := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/syslog", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		<-released // block until test releases (or request context is cancelled)
	})
	t.Cleanup(func() { close(released) })

	baseURL := newTestServer(t, mux)
	a := NewIOSAdapter(pmd3bridge.NewClient(baseURL, "test-token"))

	// Both since and until are 1 s in the past — the deadline has already
	// passed, so the context is cancelled immediately and LogRange returns.
	pastSince := time.Now().Add(-2 * time.Second)
	pastUntil := time.Now().Add(-1 * time.Second)

	started := time.Now()
	lines, err := a.LogRange("UDID", LogFilter{}, pastSince, pastUntil)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("LogRange returned error: %v", err)
	}
	// No entries — the device hasn't emitted anything in the past window.
	if len(lines) != 0 {
		t.Errorf("LogRange returned %d lines for a past window; want 0", len(lines))
	}
	// Must return promptly — within 500 ms.
	const maxWait = 500 * time.Millisecond
	if elapsed > maxWait {
		t.Errorf("LogRange took too long for a past window (elapsed=%v; want <%v)", elapsed, maxWait)
	}
}

// TestStateCache_MissDialsBridge verifies that an expired cache entry causes
// the adapter to attempt to call the bridge. Under 🎯T26.2, structured
// BridgeError responses (e.g. pmd3_error) are captured in Notes rather than
// returned as an error. Transport-level failures would panic via the client's
// fatal hook; they do not need test coverage at this layer.
func TestStateCache_MissDialsBridge(t *testing.T) {
	// Stand up a unix-socket test server that returns a pmd3_error for
	// /v1/battery, exercising the BridgeError → Notes path.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/battery", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "pmd3_error",
			"message": "test: simulated bridge error",
		})
	})
	baseURL := newTestServer(t, mux)

	a := NewIOSAdapter(pmd3bridge.NewClient(baseURL, "test-token"))
	// Prime with an expired entry.
	a.mu.Lock()
	a.cache["UDID"] = cachedState{state: State{}, at: time.Now().Add(-stateTTL - time.Second)}
	a.mu.Unlock()

	got, err := a.State("UDID")
	if err != nil {
		t.Fatalf("State err = %v; want nil (bridge errors go to Notes)", err)
	}
	// The battery call returned a structured error; Notes should capture it.
	hasNote := false
	for _, n := range got.Notes {
		if strings.Contains(n, "battery data unavailable") {
			hasNote = true
			break
		}
	}
	if !hasNote {
		t.Errorf("expected battery-data-unavailable note; got %v", got.Notes)
	}
}
