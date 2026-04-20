// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// --- reserve with selector ------------------------------------------------

// newHandlerWithReservationsAndDevices wires up a handler with:
//   - an in-memory reservation store
//   - a stub iOS adapter returning the given live devices
func newHandlerWithReservationsAndDevices(t *testing.T, iosDevices []device.Info) (*Handler, *reservations.Store) {
	t.Helper()
	s, err := reservations.New("")
	if err != nil {
		t.Fatalf("reservations.New: %v", err)
	}
	h := newTestHandler(t)
	h.ios = &stubAdapter{
		list: func() ([]device.Info, error) { return iosDevices, nil },
	}
	h.android = &stubAdapter{
		list: func() ([]device.Info, error) { return nil, nil },
	}
	h.reservations = s
	return h, s
}

func TestHandleReserve_SelectorIOS(t *testing.T) {
	// Live device: Pippa from the testInventory, with hardware UDID.
	liveDevices := []device.Info{
		{
			UUID:     "00008103-000D39301A6A201E",
			Name:     "Pippa",
			Platform: "ios",
			Model:    "iPad Air",
			OS:       "17.4",
		},
	}
	h, _ := newHandlerWithReservationsAndDevices(t, liveDevices)

	r := dispatchJSON(t, h, "reserve", map[string]any{
		"selector": `{"platform":"ios"}`,
		"owner":    "tiltbuggy",
	})
	if r.IsError {
		t.Fatalf("reserve with selector should succeed; body=%s", resultText(t, &r))
	}
	body := resultText(t, &r)
	// Should contain the device (alias "Pippa" or UUID).
	if !strings.Contains(body, "Pippa") && !strings.Contains(body, "00008103") {
		t.Errorf("reserve result should name the resolved device; got %s", body)
	}
}

func TestHandleReserve_SelectorModelFamily(t *testing.T) {
	// Two iOS devices: iPad and iPhone. Selector asks for ipad.
	liveDevices := []device.Info{
		{UUID: "ipad-uuid-physical", Name: "PadDevice", Platform: "ios", Model: "iPad Air"},
		{UUID: "iphone-uuid-physical", Name: "PhoneDevice", Platform: "ios", Model: "iPhone 15"},
	}
	h, _ := newHandlerWithReservationsAndDevices(t, liveDevices)

	r := dispatchJSON(t, h, "reserve", map[string]any{
		"selector": `{"platform":"ios","model_family":"ipad"}`,
		"owner":    "tiltbuggy",
	})
	if r.IsError {
		t.Fatalf("reserve with model_family=ipad should succeed; body=%s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "ipad-uuid-physical") {
		t.Errorf("should have selected the iPad; got %s", body)
	}
}

func TestHandleReserve_SelectorAndDevice_BothError(t *testing.T) {
	h := newTestHandler(t)
	h.reservations, _ = reservations.New("")

	r := dispatchJSON(t, h, "reserve", map[string]any{
		"device":   "Pippa",
		"selector": `{"platform":"ios"}`,
		"owner":    "tiltbuggy",
	})
	if !r.IsError {
		t.Fatal("providing both device and selector should error")
	}
}

func TestHandleReserve_NeitherDeviceNorSelector_Errors(t *testing.T) {
	h := newTestHandler(t)
	h.reservations, _ = reservations.New("")

	r := dispatchJSON(t, h, "reserve", map[string]any{
		"owner": "tiltbuggy",
	})
	if !r.IsError {
		t.Fatal("providing neither device nor selector should error")
	}
}

func TestHandleReserve_SelectorNoMatch_StructuredError(t *testing.T) {
	// Only Android devices live; iOS selector should fail with near-miss detail.
	liveDevices := []device.Info{
		{UUID: "android-serial-1", Platform: "android", Model: "Pixel 7"},
	}
	h, _ := newHandlerWithReservationsAndDevices(t, liveDevices)

	r := dispatchJSON(t, h, "reserve", map[string]any{
		"selector": `{"platform":"ios"}`,
		"owner":    "tiltbuggy",
	})
	if !r.IsError {
		t.Fatal("selector with no match should return error")
	}
	body := resultText(t, &r)
	// Should mention that no device matched or the selector details.
	if body == "" {
		t.Error("error body should not be empty")
	}
}

func TestHandleReserve_SelectorSkipsReservedDevice(t *testing.T) {
	liveDevices := []device.Info{
		{UUID: "ipad-held", Name: "HeldPad", Platform: "ios", Model: "iPad"},
		{UUID: "ipad-free", Name: "FreePad", Platform: "ios", Model: "iPad Air"},
	}
	h, s := newHandlerWithReservationsAndDevices(t, liveDevices)
	// Hold ipad-held.
	_, _ = s.Acquire("ipad-held", "someone-else", 0, "blocking")

	r := dispatchJSON(t, h, "reserve", map[string]any{
		"selector": `{"platform":"ios"}`,
		"owner":    "tiltbuggy",
	})
	if r.IsError {
		t.Fatalf("should succeed using the free device; body=%s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if strings.Contains(body, "ipad-held") {
		t.Errorf("should not have selected the held device; body=%s", body)
	}
}

func TestHandleReserve_InvalidSelectorJSON_Error(t *testing.T) {
	h := newTestHandler(t)
	h.reservations, _ = reservations.New("")

	r := dispatchJSON(t, h, "reserve", map[string]any{
		"selector": `not valid json`,
		"owner":    "tiltbuggy",
	})
	if !r.IsError {
		t.Fatal("invalid selector JSON should error")
	}
}
