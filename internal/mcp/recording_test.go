// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"strings"
	"testing"
)

// errRecordingNotSupported mirrors the error IOSAdapter.StartRecording returns.
var errRecordingNotSupported = errors.New("screen recording is not supported on iOS physical devices; use a simulator")

// --- record_start / record_stop ----------------------------------------

// newRecordingHandler returns a handler with a stub iOS adapter whose
// StartRecording immediately returns success and completes doneCh.
func newRecordingHandler(t *testing.T) *Handler {
	t.Helper()
	ios := &stubAdapter{
		startRecording: func(id, dest string) (func() error, int, error) {
			doneCh := make(chan struct{})
			close(doneCh) // Immediately done for tests.
			return func() error { return nil }, 99, nil
		},
	}
	return newHandlerWithStubs(t, ios, nil, nil)
}

func TestHandleRecordStart_HappyPath(t *testing.T) {
	h := newRecordingHandler(t)
	r := dispatchJSON(t, h, "record_start", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("record_start should succeed; body=%s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "recording started") {
		t.Errorf("expected 'recording started' in response; got %s", body)
	}
	if !strings.Contains(body, "pid") {
		t.Errorf("expected 'pid' in response; got %s", body)
	}
}

func TestHandleRecordStart_Conflict(t *testing.T) {
	h := newRecordingHandler(t)
	// First call succeeds.
	r1 := dispatchJSON(t, h, "record_start", map[string]any{"device": "Pippa"})
	if r1.IsError {
		t.Fatalf("first record_start should succeed; body=%s", resultText(t, &r1))
	}
	// Second call on same device should conflict.
	r2 := dispatchJSON(t, h, "record_start", map[string]any{"device": "Pippa"})
	if !r2.IsError {
		t.Fatalf("second record_start on same device should fail; body=%s", resultText(t, &r2))
	}
	if !strings.Contains(resultText(t, &r2), "already being recorded") {
		t.Errorf("expected conflict message; got %s", resultText(t, &r2))
	}
}

func TestHandleRecordStop_HappyPath(t *testing.T) {
	h := newRecordingHandler(t)
	// Start first.
	r1 := dispatchJSON(t, h, "record_start", map[string]any{"device": "Pippa"})
	if r1.IsError {
		t.Fatalf("record_start failed; body=%s", resultText(t, &r1))
	}
	// Stop.
	r2 := dispatchJSON(t, h, "record_stop", map[string]any{"device": "Pippa"})
	if r2.IsError {
		t.Fatalf("record_stop should succeed; body=%s", resultText(t, &r2))
	}
	if !strings.Contains(resultText(t, &r2), "recording saved to") {
		t.Errorf("expected 'recording saved to' in response; got %s", resultText(t, &r2))
	}
}

func TestHandleRecordStop_WithoutStart(t *testing.T) {
	h := newRecordingHandler(t)
	r := dispatchJSON(t, h, "record_stop", map[string]any{"device": "Pippa"})
	if !r.IsError {
		t.Fatalf("record_stop without prior record_start should fail; body=%s", resultText(t, &r))
	}
}

func TestHandleRecordStart_IOSPhysicalDeviceError(t *testing.T) {
	// Stub returns the "not supported" error that IOSAdapter.StartRecording returns.
	ios := &stubAdapter{
		startRecording: func(id, dest string) (func() error, int, error) {
			// Simulate the IOSAdapter's error.
			return nil, 0, errRecordingNotSupported
		},
	}
	h := newHandlerWithStubs(t, ios, nil, nil)
	r := dispatchJSON(t, h, "record_start", map[string]any{"device": "Pippa"})
	if !r.IsError {
		t.Fatalf("expected isError=true for iOS physical device; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "not supported") {
		t.Errorf("expected 'not supported' in error; got %s", resultText(t, &r))
	}
}

func TestHandleRecordStop_AfterSecondStartSucceeds(t *testing.T) {
	// After a stop, starting again on the same device should work.
	h := newRecordingHandler(t)
	dispatchJSON(t, h, "record_start", map[string]any{"device": "Pippa"})
	dispatchJSON(t, h, "record_stop", map[string]any{"device": "Pippa"})
	r := dispatchJSON(t, h, "record_start", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("record_start after stop should succeed; body=%s", resultText(t, &r))
	}
}
