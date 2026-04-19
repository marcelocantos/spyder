// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"strings"
	"testing"
)

func TestHandleRotate_IOSSimulator(t *testing.T) {
	calls := 0
	ios := &stubAdapter{rotate: func(id, orientation string) error {
		calls++
		if id != "C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8" {
			t.Errorf("id = %q; want simulator UUID", id)
		}
		if orientation != "landscape-left" {
			t.Errorf("orientation = %q; want landscape-left", orientation)
		}
		return nil
	}}
	h := newHandlerWithStubs(t, ios, nil, nil)
	r := dispatchJSON(t, h, "rotate", map[string]any{
		"device":      "C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8",
		"orientation": "landscape-left",
	})
	if r.IsError {
		t.Fatalf("rotate iOS simulator should succeed; body=%s", resultText(t, &r))
	}
	if calls != 1 {
		t.Errorf("Rotate calls = %d; want 1", calls)
	}
	if !strings.Contains(resultText(t, &r), "landscape-left") {
		t.Errorf("confirmation should mention orientation; body=%s", resultText(t, &r))
	}
}

func TestHandleRotate_AndroidEmulator(t *testing.T) {
	calls := 0
	android := &stubAdapter{rotate: func(id, orientation string) error {
		calls++
		if id != "emulator-5554" {
			t.Errorf("id = %q; want emulator serial", id)
		}
		if orientation != "portrait" {
			t.Errorf("orientation = %q; want portrait", orientation)
		}
		return nil
	}}
	h := newHandlerWithStubs(t, nil, android, nil)
	r := dispatchJSON(t, h, "rotate", map[string]any{
		"device":      "emulator-5554",
		"orientation": "portrait",
	})
	if r.IsError {
		t.Fatalf("rotate Android emulator should succeed; body=%s", resultText(t, &r))
	}
	if calls != 1 {
		t.Errorf("Rotate calls = %d; want 1", calls)
	}
}

func TestHandleRotate_MissingDevice(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch("rotate", map[string]any{"orientation": "portrait"})
	if err == nil {
		t.Error("Dispatch(rotate without device) should return error")
	}
}

func TestHandleRotate_MissingOrientation(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch("rotate", map[string]any{"device": "Pippa"})
	if err == nil {
		t.Error("Dispatch(rotate without orientation) should return error")
	}
}

func TestHandleRotate_AdapterError(t *testing.T) {
	ios := &stubAdapter{rotate: func(id, orientation string) error {
		return errors.New("simctl: device not found")
	}}
	h := newHandlerWithStubs(t, ios, nil, nil)
	r := dispatchJSON(t, h, "rotate", map[string]any{
		"device":      "C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8",
		"orientation": "portrait",
	})
	if !r.IsError {
		t.Fatalf("rotate should propagate adapter error; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "simctl: device not found") {
		t.Errorf("error message should include adapter error; body=%s", resultText(t, &r))
	}
}

func TestHandleRotate_ReservationGated(t *testing.T) {
	ios := &stubAdapter{rotate: func(id, orientation string) error {
		t.Fatal("Rotate should NOT be called when reservation is held by another owner")
		return nil
	}}
	h, s := newHandlerWithReservations(t, ios, nil)
	_, _ = s.Acquire("Pippa", "other-owner", 0, "test")

	r := dispatchJSON(t, h, "rotate", map[string]any{
		"device":      "Pippa",
		"orientation": "landscape-right",
		"owner":       "my-owner",
	})
	if !r.IsError {
		t.Fatalf("rotate should be blocked by reservation; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "other-owner") {
		t.Errorf("conflict error should name the holder; body=%s", resultText(t, &r))
	}
}
