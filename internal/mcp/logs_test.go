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

	r := dispatchJSON(t, h, "logs", map[string]any{"device": "iPad"})
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

	r := dispatchJSON(t, h, "logs", map[string]any{"device": "iPad"})
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
		"device": "iPad",
		"since":  "not-a-timestamp",
	})
	if !r.IsError {
		t.Errorf("expected isError=true for invalid since; got body=%s", resultText(t, &r))
	}
}

func TestHandleLogs_InvalidUntil(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device": "iPad",
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
		"device":    "iPad",
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
		"device": "iPad",
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
	_, _ = s.Acquire("iPad", "someone-else", 0, "testing")

	// logs should proceed even though the device is reserved by someone else.
	r := dispatchJSON(t, h, "logs", map[string]any{"device": "iPad"})
	if r.IsError {
		t.Fatalf("logs should be read-only and unaffected by reservations; body=%s", resultText(t, &r))
	}
}

// --- bundle_id resolution --------------------------------------------

func TestHandleLogs_BundleIDResolved(t *testing.T) {
	var capturedFilter device.LogFilter
	ios := &stubAdapter{
		resolveExecutable: func(id, bundle string) (string, bool, error) {
			if bundle != "com.squz.multimaze2" {
				t.Errorf("ResolveExecutable bundle = %q; want com.squz.multimaze2", bundle)
			}
			return "MultiMaze2", true, nil
		},
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			capturedFilter = filter
			return nil, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.squz.multimaze2",
	})
	if r.IsError {
		t.Fatalf("logs should succeed; body=%s", resultText(t, &r))
	}
	if capturedFilter.Process != "MultiMaze2" {
		t.Errorf("filter.Process = %q; want MultiMaze2 (resolved from bundle_id)", capturedFilter.Process)
	}
}

func TestHandleLogs_BundleIDAndProcessRejected(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.squz.multimaze2",
		"process":   "MultiMaze2",
	})
	if !r.IsError {
		t.Errorf("expected isError=true when both bundle_id and process set; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion; body=%s", resultText(t, &r))
	}
}

func TestHandleLogs_BundleIDNotInstalled(t *testing.T) {
	ios := &stubAdapter{
		resolveExecutable: func(id, bundle string) (string, bool, error) {
			return "", false, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.example.ghost",
	})
	if !r.IsError {
		t.Errorf("expected isError=true for uninstalled bundle; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "not installed") {
		t.Errorf("error should mention 'not installed'; body=%s", resultText(t, &r))
	}
}

// --- since=launch -----------------------------------------------------

func TestHandleLogs_SinceLaunch(t *testing.T) {
	var capturedSince time.Time
	ios := &stubAdapter{
		resolveExecutable: func(id, bundle string) (string, bool, error) {
			return "MultiMaze", true, nil
		},
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			capturedSince = since
			return nil, nil
		},
		launchApp: func(id, bundle string) error { return nil },
	}
	h := newHandlerWithStubs(t, ios, nil)

	// First call launch_app to seed launchTimes.
	before := time.Now()
	r := dispatchJSON(t, h, "launch_app", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.squz.multimaze2",
	})
	if r.IsError {
		t.Fatalf("launch_app should succeed; body=%s", resultText(t, &r))
	}
	after := time.Now()

	// Now logs with since=launch should resolve to the launch time.
	r = dispatchJSON(t, h, "logs", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.squz.multimaze2",
		"since":     "launch",
	})
	if r.IsError {
		t.Fatalf("logs should succeed; body=%s", resultText(t, &r))
	}
	if capturedSince.Before(before) || capturedSince.After(after) {
		t.Errorf("since = %v; want between %v and %v", capturedSince, before, after)
	}
}

func TestHandleLogs_SinceLaunchRequiresBundleID(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device": "iPad",
		"since":  "launch",
	})
	if !r.IsError {
		t.Fatalf("expected isError=true; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "bundle_id") {
		t.Errorf("error should mention bundle_id; body=%s", resultText(t, &r))
	}
}

func TestHandleLogs_SinceLaunchUnknown(t *testing.T) {
	ios := &stubAdapter{
		resolveExecutable: func(id, bundle string) (string, bool, error) {
			return "MultiMaze", true, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.squz.multimaze2",
		"since":     "launch",
	})
	if !r.IsError {
		t.Fatalf("expected isError=true for un-launched bundle; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "no launch_app call recorded") {
		t.Errorf("error should mention missing launch record; body=%s", resultText(t, &r))
	}
}

// --- ResolveAdapterForStream ------------------------------------------

func TestResolveAdapterForStream(t *testing.T) {
	h := newTestHandler(t)
	adapter, id, err := h.ResolveAdapterForStream("iPad")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if adapter == nil {
		t.Error("adapter is nil")
	}
	if id != "00008103-001122334455667A" {
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
