// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"strings"
	"testing"
)

// TestHandleRotate_IOSSimulator verifies that the rotate handler
// calls Rotate on the adapter with the right id and orientation and
// returns a success message.
func TestHandleRotate_IOSSimulator(t *testing.T) {
	called := false
	var calledID, calledOrientation string
	ios := &stubAdapter{rotate: func(id, orientation string) error {
		called = true
		calledID = id
		calledOrientation = orientation
		return nil
	}}
	h := newHandlerWithStubs(t, ios, nil, nil)

	r := dispatchJSON(t, h, "rotate", map[string]any{
		"device":      "Pippa",
		"orientation": "landscape-left",
	})
	if r.IsError {
		t.Fatalf("rotate should succeed; body=%s", resultText(t, &r))
	}
	if !called {
		t.Error("Rotate was not called on the adapter")
	}
	// Pippa resolves to ios_uuid 00008103-000D39301A6A201E.
	if calledID != "00008103-000D39301A6A201E" {
		t.Errorf("Rotate called with id=%q; want iOS hardware UDID", calledID)
	}
	if calledOrientation != "landscape-left" {
		t.Errorf("Rotate called with orientation=%q; want landscape-left", calledOrientation)
	}
	text := resultText(t, &r)
	if !strings.Contains(text, "Pippa") || !strings.Contains(text, "landscape-left") {
		t.Errorf("success message missing device/orientation; body=%s", text)
	}
}

// TestHandleRotate_MissingDevice verifies that rotate returns an error
// when device is not provided.
func TestHandleRotate_MissingDevice(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch("rotate", map[string]any{"orientation": "portrait"})
	if err == nil {
		t.Error("Dispatch(rotate without device) returned nil; want error")
	}
}

// TestHandleRotate_MissingOrientation verifies that rotate returns an error
// when orientation is not provided.
func TestHandleRotate_MissingOrientation(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch("rotate", map[string]any{"device": "Pippa"})
	if err == nil {
		t.Error("Dispatch(rotate without orientation) returned nil; want error")
	}
}

// TestHandleRotate_AdapterError verifies that adapter errors are surfaced
// as tool errors (isError=true) rather than transport errors.
func TestHandleRotate_AdapterError(t *testing.T) {
	ios := &stubAdapter{rotate: func(id, orientation string) error {
		return errors.New("rotation on real iOS devices is not supported")
	}}
	h := newHandlerWithStubs(t, ios, nil, nil)

	r := dispatchJSON(t, h, "rotate", map[string]any{
		"device":      "Pippa",
		"orientation": "portrait",
	})
	if !r.IsError {
		t.Fatalf("expected isError=true for adapter error; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "not supported") {
		t.Errorf("expected error message about not supported; body=%s", resultText(t, &r))
	}
}

// TestHandleRotate_ReservationGated verifies that rotate is blocked
// when the device is reserved by a different owner.
func TestHandleRotate_ReservationGated(t *testing.T) {
	called := false
	ios := &stubAdapter{rotate: func(id, orientation string) error {
		called = true
		return nil
	}}
	h, resv, _ := newHandlerWithRuns(t, ios, nil)

	// Someone else reserves Pippa.
	_, err := resv.Acquire("Pippa", "other-owner", 0, "blocking")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	r := dispatchJSON(t, h, "rotate", map[string]any{
		"device":      "Pippa",
		"orientation": "portrait",
		"owner":       "my-owner",
	})
	if !r.IsError {
		t.Fatalf("rotate should be blocked by reservation; body=%s", resultText(t, &r))
	}
	if called {
		t.Error("Rotate should not have been called when reservation is held by another owner")
	}
}

// TestHandleRotate_AndroidEmulator verifies that the rotate handler
// dispatches to the android adapter for Android serials.
func TestHandleRotate_AndroidEmulator(t *testing.T) {
	called := false
	android := &stubAdapter{rotate: func(id, orientation string) error {
		called = true
		return nil
	}}
	h := newHandlerWithStubs(t, nil, android, nil)

	r := dispatchJSON(t, h, "rotate", map[string]any{
		"device":      "Raspberry",
		"orientation": "landscape-right",
	})
	if r.IsError {
		t.Fatalf("rotate on Android emulator should succeed; body=%s", resultText(t, &r))
	}
	if !called {
		t.Error("Rotate was not called on Android adapter")
	}
}
