// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// stubAdapter is an in-memory device.Adapter for handler tests.
// Each method defers to a function field so tests can shape behaviour
// (success, error, platform-specific return values) without exec.
type stubAdapter struct {
	list            func() ([]device.Info, error)
	state           func(id string) (device.State, error)
	launchKeepAwake func(id string) error
	screenshot      func(id string) ([]byte, error)
	listApps        func(id string) ([]device.AppInfo, error)
	launchApp       func(id, bundle string) error
	terminateApp    func(id, bundle string) error
	rotate          func(id, orientation string) error
	crashes         func(id string, since time.Time, process string) ([]device.CrashReport, error)
}

func (s *stubAdapter) List() ([]device.Info, error) {
	if s.list == nil {
		return nil, nil
	}
	return s.list()
}
func (s *stubAdapter) State(id string) (device.State, error) {
	if s.state == nil {
		return device.State{}, nil
	}
	return s.state(id)
}
func (s *stubAdapter) LaunchKeepAwake(id string) error {
	if s.launchKeepAwake == nil {
		return nil
	}
	return s.launchKeepAwake(id)
}
func (s *stubAdapter) Screenshot(id string) ([]byte, error) {
	if s.screenshot == nil {
		return nil, nil
	}
	return s.screenshot(id)
}
func (s *stubAdapter) ListApps(id string) ([]device.AppInfo, error) {
	if s.listApps == nil {
		return nil, nil
	}
	return s.listApps(id)
}
func (s *stubAdapter) LaunchApp(id, bundle string) error {
	if s.launchApp == nil {
		return nil
	}
	return s.launchApp(id, bundle)
}
func (s *stubAdapter) TerminateApp(id, bundle string) error {
	if s.terminateApp == nil {
		return nil
	}
	return s.terminateApp(id, bundle)
}
func (s *stubAdapter) Rotate(id, orientation string) error {
	if s.rotate == nil {
		return nil
	}
	return s.rotate(id, orientation)
}
func (s *stubAdapter) Crashes(id string, since time.Time, process string) ([]device.CrashReport, error) {
	if s.crashes == nil {
		return nil, nil
	}
	return s.crashes(id, since, process)
}

// stubTunneld is a TunneldGate with controllable Require behaviour.
type stubTunneld struct {
	requireErr error
}

func (s *stubTunneld) Require() error { return s.requireErr }
func (s *stubTunneld) Addr() string   { return "stub:0" }

// newHandlerWithStubs returns a Handler wired up with the given stubs.
// The inventory is the one newTestHandler would populate (via HOME).
func newHandlerWithStubs(t *testing.T, ios, android device.Adapter, tun TunneldGate) *Handler {
	t.Helper()
	h := newTestHandler(t) // sets HOME, loads testInventory
	if ios != nil {
		h.ios = ios
	}
	if android != nil {
		h.android = android
	}
	h.tunneld = tun
	return h
}

func resultText(t *testing.T, r *callToolResultForTest) string {
	t.Helper()
	if len(r.Content) == 0 {
		t.Fatal("result has no content")
	}
	return r.Content[0].Text
}

// callToolResultForTest mirrors the subset of mcp-go's CallToolResult
// we inspect. Declaring it avoids importing mcp-go just to pull out
// these fields.
type callToolResultForTest struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
		Data string `json:"data,omitempty"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// dispatchJSON runs h.Dispatch and reinterprets the returned
// *mcpgo.CallToolResult as callToolResultForTest via JSON round-trip.
// Reliable and avoids coupling to mcp-go's internal types.
func dispatchJSON(t *testing.T, h *Handler, name string, args map[string]any) callToolResultForTest {
	t.Helper()
	res, err := h.Dispatch(name, args)
	if err != nil {
		t.Fatalf("Dispatch(%s) err = %v", name, err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out callToolResultForTest
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out
}

// --- handleDevices -----------------------------------------------------

func TestHandleDevices_All_BothOK(t *testing.T) {
	ios := &stubAdapter{list: func() ([]device.Info, error) {
		return []device.Info{{UUID: "00008103-000D39301A6A201E", Platform: "ios"}}, nil
	}}
	android := &stubAdapter{list: func() ([]device.Info, error) {
		return []device.Info{{UUID: "R5CR112X76K", Platform: "android"}}, nil
	}}
	h := newHandlerWithStubs(t, ios, android, nil)

	r := dispatchJSON(t, h, "devices", map[string]any{"platform": "all"})
	if r.IsError {
		t.Fatalf("isError true; body=%s", resultText(t, &r))
	}
	// The "all" with no errors returns the array directly (not wrapped).
	text := resultText(t, &r)
	if !strings.Contains(text, "00008103-000D39301A6A201E") || !strings.Contains(text, "R5CR112X76K") {
		t.Errorf("missing expected UDIDs; body=%s", text)
	}
	// Alias annotation should come in from testInventory.
	if !strings.Contains(text, `"alias": "Pippa"`) {
		t.Errorf("iOS alias not annotated from inventory; body=%s", text)
	}
}

func TestHandleDevices_All_IOSErrors_SurfacedInWrapper(t *testing.T) {
	ios := &stubAdapter{list: func() ([]device.Info, error) {
		return nil, errors.New("ios adapter broken")
	}}
	android := &stubAdapter{list: func() ([]device.Info, error) {
		return []device.Info{{UUID: "R5CR112X76K", Platform: "android"}}, nil
	}}
	h := newHandlerWithStubs(t, ios, android, nil)

	r := dispatchJSON(t, h, "devices", map[string]any{"platform": "all"})
	if r.IsError {
		t.Fatalf("platform=all should not hard-fail when only one adapter errors; body=%s", resultText(t, &r))
	}
	text := resultText(t, &r)
	if !strings.Contains(text, "R5CR112X76K") {
		t.Errorf("android devices should still be present; body=%s", text)
	}
	if !strings.Contains(text, "ios adapter broken") {
		t.Errorf("iOS error should be surfaced in wrapper; body=%s", text)
	}
}

func TestHandleDevices_IOSOnly_HardFailOnError(t *testing.T) {
	ios := &stubAdapter{list: func() ([]device.Info, error) {
		return nil, errors.New("boom")
	}}
	h := newHandlerWithStubs(t, ios, nil, nil)

	r := dispatchJSON(t, h, "devices", map[string]any{"platform": "ios"})
	if !r.IsError {
		t.Fatalf("platform=ios with iOS error should hard-fail; body=%s", resultText(t, &r))
	}
}

// --- handleResolve -----------------------------------------------------

func TestHandleResolve_Known(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "resolve", map[string]any{"name": "Pippa"})
	if r.IsError {
		t.Fatalf("resolve Pippa should succeed; body=%s", resultText(t, &r))
	}
	text := resultText(t, &r)
	if !strings.Contains(text, "00008103-000D39301A6A201E") {
		t.Errorf("resolve Pippa body missing iOS UDID; body=%s", text)
	}
	if !strings.Contains(text, "E1A01EA6-8D77-556C-B18D-D470B2909E87") {
		t.Errorf("resolve Pippa body missing CoreDevice UUID; body=%s", text)
	}
}

func TestHandleResolve_Passthrough(t *testing.T) {
	h := newTestHandler(t)
	// Unknown raw UDID that looks iOS → classified as iOS, echoed in ios_uuid.
	r := dispatchJSON(t, h, "resolve", map[string]any{"name": "ABCDEF12-1234567890ABCDEF"})
	if r.IsError {
		t.Fatalf("resolve unknown should passthrough, not error; body=%s", resultText(t, &r))
	}
	text := resultText(t, &r)
	if !strings.Contains(text, "ABCDEF12-1234567890ABCDEF") {
		t.Errorf("passthrough did not echo input; body=%s", text)
	}
}

// --- handleKeepAwake ---------------------------------------------------

func TestHandleKeepAwake_IOS(t *testing.T) {
	ios := &stubAdapter{launchKeepAwake: func(id string) error { return nil }}
	h := newHandlerWithStubs(t, ios, nil, nil)
	r := dispatchJSON(t, h, "keepawake", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("keepawake iOS should succeed; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "KeepAwake launched on Pippa") {
		t.Errorf("iOS keepawake message wrong; body=%s", resultText(t, &r))
	}
}

func TestHandleKeepAwake_Android_NoOpMessage(t *testing.T) {
	android := &stubAdapter{launchKeepAwake: func(id string) error { return nil }}
	h := newHandlerWithStubs(t, nil, android, nil)
	r := dispatchJSON(t, h, "keepawake", map[string]any{"device": "Raspberry"})
	if r.IsError {
		t.Fatalf("keepawake Android should succeed (no-op); body=%s", resultText(t, &r))
	}
	text := resultText(t, &r)
	if !strings.Contains(text, "no-op on Raspberry") || !strings.Contains(text, "Stay awake while plugged in") {
		t.Errorf("Android keepawake should point at OS setting; body=%s", text)
	}
}

// --- handleDeviceState -------------------------------------------------

func TestHandleDeviceState(t *testing.T) {
	battery := 87
	charging := true
	ios := &stubAdapter{state: func(id string) (device.State, error) {
		return device.State{BatteryLevel: &battery, Charging: &charging}, nil
	}}
	h := newHandlerWithStubs(t, ios, nil, nil)
	r := dispatchJSON(t, h, "device_state", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("device_state should succeed; body=%s", resultText(t, &r))
	}
	text := resultText(t, &r)
	if !strings.Contains(text, `"battery_level": 87`) {
		t.Errorf("missing battery_level; body=%s", text)
	}
	if !strings.Contains(text, `"charging": true`) {
		t.Errorf("missing charging; body=%s", text)
	}
}

// --- handleScreenshot --------------------------------------------------

func TestHandleScreenshot_ReturnsImage(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
	ios := &stubAdapter{screenshot: func(id string) ([]byte, error) { return png, nil }}
	h := newHandlerWithStubs(t, ios, nil, &stubTunneld{}) // tunneld.Require() returns nil

	r := dispatchJSON(t, h, "screenshot", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("screenshot should succeed; body=%v", r)
	}
	var img *struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
		Data string `json:"data,omitempty"`
	}
	for i := range r.Content {
		if r.Content[i].Type == "image" {
			img = &r.Content[i]
			break
		}
	}
	if img == nil {
		t.Fatalf("expected image content block; got %+v", r.Content)
	}
	if img.Data == "" {
		t.Error("image content has empty data")
	}
}

func TestHandleScreenshot_TunneldGate(t *testing.T) {
	ios := &stubAdapter{screenshot: func(id string) ([]byte, error) {
		t.Fatal("Screenshot should NOT be called when tunneld.Require fails")
		return nil, nil
	}}
	h := newHandlerWithStubs(t, ios, nil, &stubTunneld{requireErr: errors.New("tunneld down")})
	r := dispatchJSON(t, h, "screenshot", map[string]any{"device": "Pippa"})
	if !r.IsError {
		t.Fatalf("expected isError=true when tunneld gate fails; got %+v", r)
	}
	if !strings.Contains(resultText(t, &r), "tunneld down") {
		t.Errorf("expected tunneld error in body; got %s", resultText(t, &r))
	}
}

// --- handleListApps, handleLaunchApp, handleTerminateApp ---------------

func TestHandleListApps(t *testing.T) {
	ios := &stubAdapter{listApps: func(id string) ([]device.AppInfo, error) {
		return []device.AppInfo{
			{BundleID: "com.foo.bar", Name: "Foo", Version: "1.0"},
			{BundleID: "com.baz.qux"},
		}, nil
	}}
	h := newHandlerWithStubs(t, ios, nil, nil)
	r := dispatchJSON(t, h, "list_apps", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("list_apps should succeed; body=%s", resultText(t, &r))
	}
	text := resultText(t, &r)
	if !strings.Contains(text, "com.foo.bar") || !strings.Contains(text, "com.baz.qux") {
		t.Errorf("missing bundle ids; body=%s", text)
	}
}

func TestHandleLaunchApp_TunneldGateOnIOS(t *testing.T) {
	ios := &stubAdapter{launchApp: func(id, bundle string) error {
		t.Fatal("LaunchApp should NOT be called when tunneld gate fails")
		return nil
	}}
	h := newHandlerWithStubs(t, ios, nil, &stubTunneld{requireErr: errors.New("tunneld down")})
	r := dispatchJSON(t, h, "launch_app", map[string]any{"device": "Pippa", "bundle_id": "com.foo"})
	if !r.IsError {
		t.Fatalf("expected isError=true when tunneld gate fails; got %+v", r)
	}
}

func TestHandleLaunchApp_AndroidNoTunneldGate(t *testing.T) {
	calls := 0
	android := &stubAdapter{launchApp: func(id, bundle string) error {
		calls++
		return nil
	}}
	// Note: tunneld.Require would fail, but it's not called for Android.
	h := newHandlerWithStubs(t, nil, android, &stubTunneld{requireErr: errors.New("tunneld down")})
	r := dispatchJSON(t, h, "launch_app", map[string]any{"device": "Raspberry", "bundle_id": "com.foo"})
	if r.IsError {
		t.Fatalf("Android launch_app shouldn't be gated by tunneld; body=%s", resultText(t, &r))
	}
	if calls != 1 {
		t.Errorf("LaunchApp calls = %d; want 1", calls)
	}
}

func TestHandleTerminateApp(t *testing.T) {
	called := false
	android := &stubAdapter{terminateApp: func(id, bundle string) error {
		called = true
		if bundle != "com.squz.tiltbuggy" {
			t.Errorf("bundle = %q; want com.squz.tiltbuggy", bundle)
		}
		return nil
	}}
	h := newHandlerWithStubs(t, nil, android, nil)
	r := dispatchJSON(t, h, "terminate_app", map[string]any{
		"device":    "Raspberry",
		"bundle_id": "com.squz.tiltbuggy",
	})
	if r.IsError {
		t.Fatalf("terminate_app should succeed; body=%s", resultText(t, &r))
	}
	if !called {
		t.Error("TerminateApp was not called")
	}
	if !strings.Contains(resultText(t, &r), "terminated com.squz.tiltbuggy on Raspberry") {
		t.Errorf("unexpected body: %s", resultText(t, &r))
	}
}

// --- argument validation ----------------------------------------------

func TestHandleLaunchApp_MissingBundleID(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch("launch_app", map[string]any{"device": "Pippa"})
	if err == nil {
		t.Error("Dispatch(launch_app without bundle_id) returned nil; want error")
	}
}
