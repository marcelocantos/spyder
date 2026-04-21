// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// newHandlerWithReservations returns a Handler wired up with a real
// reservation Store (in-memory; no file) plus the given stubs.
func newHandlerWithReservations(t *testing.T, ios, android device.Adapter) (*Handler, *reservations.Store) {
	t.Helper()
	s, err := reservations.New("") // in-memory
	if err != nil {
		t.Fatalf("reservations.New: %v", err)
	}
	h := newTestHandler(t)
	if ios != nil {
		h.ios = ios
	}
	if android != nil {
		h.android = android
	}
	h.reservations = s
	return h, s
}

// --- reserve / release / renew / reservations ------------------------

func TestHandleReserve_HappyPath(t *testing.T) {
	h, _ := newHandlerWithReservations(t, nil, nil)
	r := dispatchJSON(t, h, "reserve", map[string]any{
		"device":      "Pippa",
		"owner":       "tiltbuggy",
		"ttl_seconds": 60.0,
		"note":        "unit test",
	})
	if r.IsError {
		t.Fatalf("reserve should succeed; body=%s", resultText(t, &r))
	}
	body := resultText(t, &r)
	for _, want := range []string{"Pippa", "tiltbuggy", "unit test", "expires_at"} {
		if !strings.Contains(body, want) {
			t.Errorf("reserve body missing %q: %s", want, body)
		}
	}
}

func TestHandleReserve_Conflict(t *testing.T) {
	h, s := newHandlerWithReservations(t, nil, nil)
	_, _ = s.Acquire("Pippa", "someone-else", 0, "testing")

	r := dispatchJSON(t, h, "reserve", map[string]any{
		"device": "Pippa",
		"owner":  "tiltbuggy",
	})
	if !r.IsError {
		t.Fatal("reserve of already-held device should be an error")
	}
	if !strings.Contains(resultText(t, &r), "someone-else") {
		t.Errorf("conflict should name the holder; got %s", resultText(t, &r))
	}
}

func TestHandleRelease_ByOwner(t *testing.T) {
	h, s := newHandlerWithReservations(t, nil, nil)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")

	r := dispatchJSON(t, h, "release", map[string]any{
		"device": "Pippa",
		"owner":  "tiltbuggy",
	})
	if r.IsError {
		t.Fatalf("owner release should succeed; body=%s", resultText(t, &r))
	}
	if _, held := s.Get("Pippa"); held {
		t.Error("reservation should be gone after release")
	}
}

func TestHandleRelease_NonOwner_Conflicts(t *testing.T) {
	h, s := newHandlerWithReservations(t, nil, nil)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")

	r := dispatchJSON(t, h, "release", map[string]any{
		"device": "Pippa",
		"owner":  "otherproj",
	})
	if !r.IsError {
		t.Fatal("non-owner release should conflict")
	}
}

func TestHandleRenew(t *testing.T) {
	h, s := newHandlerWithReservations(t, nil, nil)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")

	r := dispatchJSON(t, h, "renew", map[string]any{
		"device":      "Pippa",
		"owner":       "tiltbuggy",
		"ttl_seconds": 7200.0,
	})
	if r.IsError {
		t.Fatalf("renew should succeed; body=%s", resultText(t, &r))
	}
}

func TestHandleReservations_List(t *testing.T) {
	h, s := newHandlerWithReservations(t, nil, nil)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")
	_, _ = s.Acquire("Raspberry", "otherproj", 0, "")

	r := dispatchJSON(t, h, "reservations", map[string]any{})
	if r.IsError {
		t.Fatalf("reservations should succeed; body=%s", resultText(t, &r))
	}
	body := resultText(t, &r)
	for _, want := range []string{"Pippa", "Raspberry", "tiltbuggy", "otherproj"} {
		if !strings.Contains(body, want) {
			t.Errorf("list missing %q: %s", want, body)
		}
	}
}

func TestHandleReservations_NoStore_EmptyList(t *testing.T) {
	h := newTestHandler(t) // no reservations store
	r := dispatchJSON(t, h, "reservations", map[string]any{})
	if r.IsError {
		t.Fatalf("reservations without store should return empty list, not error; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "[]") {
		t.Errorf("expected empty list; got %s", resultText(t, &r))
	}
}

// --- strict enforcement on mutating tools -----------------------------

func TestHandleLaunchApp_RejectsWhenHeld(t *testing.T) {
	ios := &stubAdapter{launchApp: func(id, bundle string) error {
		t.Fatal("LaunchApp should NOT be called")
		return nil
	}}
	h, s := newHandlerWithReservations(t, ios, nil)
	_, _ = s.Acquire("Pippa", "someone-else", 0, "")

	r := dispatchJSON(t, h, "launch_app", map[string]any{
		"device":    "Pippa",
		"bundle_id": "com.foo",
	})
	if !r.IsError {
		t.Fatal("launch_app on reserved device should fail")
	}
}

func TestHandleTerminateApp_RejectsWhenHeld(t *testing.T) {
	ios := &stubAdapter{terminateApp: func(id, bundle string) error {
		t.Fatal("TerminateApp should NOT be called")
		return nil
	}}
	h, s := newHandlerWithReservations(t, ios, nil)
	_, _ = s.Acquire("Pippa", "someone-else", 0, "")

	r := dispatchJSON(t, h, "terminate_app", map[string]any{
		"device":    "Pippa",
		"bundle_id": "com.foo",
	})
	if !r.IsError {
		t.Fatal("terminate_app on reserved device should fail")
	}
}

func TestHandleScreenshot_RejectsWhenHeld(t *testing.T) {
	ios := &stubAdapter{screenshot: func(id string) ([]byte, error) {
		t.Fatal("Screenshot should NOT be called")
		return nil, nil
	}}
	h, s := newHandlerWithReservations(t, ios, nil)
	_, _ = s.Acquire("Pippa", "someone-else", 0, "")

	r := dispatchJSON(t, h, "screenshot", map[string]any{"device": "Pippa"})
	if !r.IsError {
		t.Fatal("screenshot on reserved device should fail")
	}
}

// --- read tools ignore reservations -----------------------------------

func TestReadTools_IgnoreReservations(t *testing.T) {
	battery := 87
	charging := true
	ios := &stubAdapter{
		state: func(id string) (device.State, error) {
			return device.State{BatteryLevel: &battery, Charging: &charging}, nil
		},
	}
	h, s := newHandlerWithReservations(t, ios, nil)
	_, _ = s.Acquire("Pippa", "someone-else", 0, "")

	// device_state is read-only → should proceed without owner.
	r := dispatchJSON(t, h, "device_state", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Errorf("device_state should be read-only and unaffected by reservations; body=%s", resultText(t, &r))
	}

	// resolve is read-only too.
	r = dispatchJSON(t, h, "resolve", map[string]any{"name": "Pippa"})
	if r.IsError {
		t.Errorf("resolve should be read-only; body=%s", resultText(t, &r))
	}
}

// --- anonymous calls on held device -----------------------------------

func TestMutatingTool_AnonymousCaller_Rejected(t *testing.T) {
	ios := &stubAdapter{screenshot: func(id string) ([]byte, error) { return nil, nil }}
	h, s := newHandlerWithReservations(t, ios, nil)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")

	// No owner arg → treated as anonymous → rejected because device is held.
	r := dispatchJSON(t, h, "screenshot", map[string]any{"device": "Pippa"})
	if !r.IsError {
		t.Fatal("anonymous mutating call on held device should reject")
	}
}

func TestMutatingTool_AnonymousCaller_FreeDevice_Proceeds(t *testing.T) {
	called := false
	ios := &stubAdapter{screenshot: func(id string) ([]byte, error) {
		called = true
		return []byte("PNG"), nil
	}}
	h, _ := newHandlerWithReservations(t, ios, nil)

	// No reservation → anonymous caller proceeds.
	r := dispatchJSON(t, h, "screenshot", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("free-device anonymous call should succeed; body=%s", resultText(t, &r))
	}
	if !called {
		t.Error("adapter should have been called")
	}
}
