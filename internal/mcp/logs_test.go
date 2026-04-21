// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

func TestHandleLogs_HappyPath(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	lines := []device.LogLine{
		{Timestamp: now, Process: "MyApp", Level: "error", Message: "crash happened"},
		{Timestamp: now.Add(time.Second), Process: "MyApp", Level: "info", Message: "recovered"},
	}
	ios := &stubAdapter{
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			return lines, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)

	r := dispatchJSON(t, h, "logs", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("logs should succeed; body=%s", resultText(t, &r))
	}
	text := resultText(t, &r)
	if !strings.Contains(text, "crash happened") {
		t.Errorf("missing first log line; body=%s", text)
	}
	if !strings.Contains(text, "recovered") {
		t.Errorf("missing second log line; body=%s", text)
	}
}

func TestHandleLogs_EmptyResult(t *testing.T) {
	ios := &stubAdapter{
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			return nil, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)

	r := dispatchJSON(t, h, "logs", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("logs with empty result should succeed; body=%s", resultText(t, &r))
	}
	text := resultText(t, &r)
	// Nil result is normalised to an empty JSON array.
	if !strings.Contains(text, "[]") {
		t.Errorf("expected empty array; body=%s", text)
	}
}

func TestHandleLogs_MissingDevice(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch("logs", map[string]any{})
	if err == nil {
		t.Error("Dispatch(logs, {}) returned nil err; want error for missing device")
	}
}

func TestHandleLogs_InvalidSince(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device": "Pippa",
		"since":  "not-a-timestamp",
	})
	if !r.IsError {
		t.Errorf("expected isError=true for invalid since; got body=%s", resultText(t, &r))
	}
}

func TestHandleLogs_InvalidUntil(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device": "Pippa",
		"until":  "2026/04/19",
	})
	if !r.IsError {
		t.Errorf("expected isError=true for invalid until; got body=%s", resultText(t, &r))
	}
}

func TestHandleLogs_FilterPassthrough(t *testing.T) {
	var capturedFilter device.LogFilter
	ios := &stubAdapter{
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			capturedFilter = filter
			return nil, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)

	r := dispatchJSON(t, h, "logs", map[string]any{
		"device":    "Pippa",
		"process":   "MyApp",
		"subsystem": "com.example",
		"regex":     "error.*",
	})
	if r.IsError {
		t.Fatalf("logs should succeed; body=%s", resultText(t, &r))
	}
	if capturedFilter.Process != "MyApp" {
		t.Errorf("Process = %q; want MyApp", capturedFilter.Process)
	}
	if capturedFilter.Subsystem != "com.example" {
		t.Errorf("Subsystem = %q; want com.example", capturedFilter.Subsystem)
	}
	if capturedFilter.Regex != "error.*" {
		t.Errorf("Regex = %q; want 'error.*'", capturedFilter.Regex)
	}
}

func TestHandleLogs_TimestampPassthrough(t *testing.T) {
	var capturedSince, capturedUntil time.Time
	ios := &stubAdapter{
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			capturedSince = since
			capturedUntil = until
			return nil, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)

	sinceStr := "2026-04-19T00:00:00Z"
	untilStr := "2026-04-19T01:00:00Z"
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device": "Pippa",
		"since":  sinceStr,
		"until":  untilStr,
	})
	if r.IsError {
		t.Fatalf("logs should succeed; body=%s", resultText(t, &r))
	}
	wantSince, _ := time.Parse(time.RFC3339, sinceStr)
	wantUntil, _ := time.Parse(time.RFC3339, untilStr)
	if !capturedSince.Equal(wantSince) {
		t.Errorf("since = %v; want %v", capturedSince, wantSince)
	}
	if !capturedUntil.Equal(wantUntil) {
		t.Errorf("until = %v; want %v", capturedUntil, wantUntil)
	}
}

// TestHandleLogs_ReadOnly verifies that logs is not subject to reservation checks
// (it is a read-only tool).
func TestHandleLogs_ReadOnly(t *testing.T) {
	ios := &stubAdapter{
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			return []device.LogLine{{Message: "ok"}}, nil
		},
	}
	h, s := newHandlerWithReservations(t, ios, nil)
	_, _ = s.Acquire("Pippa", "someone-else", 0, "testing")

	// logs should proceed even though the device is reserved by someone else.
	r := dispatchJSON(t, h, "logs", map[string]any{"device": "Pippa"})
	if r.IsError {
		t.Fatalf("logs should be read-only and unaffected by reservations; body=%s", resultText(t, &r))
	}
}

// --- ResolveAdapterForStream ------------------------------------------

func TestResolveAdapterForStream(t *testing.T) {
	h := newTestHandler(t)
	adapter, id, err := h.ResolveAdapterForStream("Pippa")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if adapter == nil {
		t.Error("adapter is nil")
	}
	if id != "00008103-000D39301A6A201E" {
		t.Errorf("id = %q; want iOS UDID", id)
	}
}

// --- Compile-time: stubAdapter satisfies device.Adapter ---------------
var _ device.Adapter = (*stubAdapter)(nil)

// Verify LogStream signature matches the interface.
var _ = func() {
	var s stubAdapter
	ctx := context.Background()
	out := make(chan device.LogLine)
	_ = s.LogStream(ctx, "", device.LogFilter{}, out)
}
