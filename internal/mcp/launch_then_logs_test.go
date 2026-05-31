// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// TestLaunchThenLogs_HappyPath is the headline E2E test for the
// launch_app → logs(since=launch) workflow introduced in v0.36–v0.38.
// It verifies:
//   - launch_app succeeds and records a timestamp
//   - subsequent logs(since=launch, bundle_id=...) passes that timestamp as
//     the since argument to LogRange
//   - filter.Process matches the executable name returned by ResolveExecutable
//   - the returned lines reach the caller (IsError=false, JSON array)
func TestLaunchThenLogs_HappyPath(t *testing.T) {
	const bundleID = "com.squz.multimaze2"
	const exeName = "MultiMaze2"

	var capturedSince time.Time
	var capturedFilter device.LogFilter

	logLines := []device.LogLine{
		{Timestamp: time.Now().UTC().Truncate(time.Second), Process: exeName, Level: "info", Message: "app started"},
		{Timestamp: time.Now().UTC().Add(time.Second).Truncate(time.Second), Process: exeName, Level: "debug", Message: "frame rendered"},
	}

	ios := &stubAdapter{
		resolveExecutable: func(id, bundle string) (string, bool, error) {
			if bundle != bundleID {
				t.Errorf("ResolveExecutable bundle = %q; want %s", bundle, bundleID)
			}
			return exeName, true, nil
		},
		launchApp: func(id, bundle string, env map[string]string) error { return nil },
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			capturedFilter = filter
			capturedSince = since
			return logLines, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)

	// Bracket the launch_app call with wall-clock bounds.
	before := time.Now()
	r := dispatchJSON(t, h, "launch_app", map[string]any{
		"device":    "iPad",
		"bundle_id": bundleID,
	})
	after := time.Now()

	if r.IsError {
		t.Fatalf("launch_app should succeed; body=%s", resultText(t, &r))
	}

	// Now fetch logs with since=launch.
	r = dispatchJSON(t, h, "logs", map[string]any{
		"device":    "iPad",
		"bundle_id": bundleID,
		"since":     "launch",
	})
	if r.IsError {
		t.Fatalf("logs(since=launch) should succeed; body=%s", resultText(t, &r))
	}

	// The since timestamp passed to LogRange must fall within the launch window.
	if capturedSince.Before(before) || capturedSince.After(after) {
		t.Errorf("since = %v; want between %v and %v (the launch_app call window)",
			capturedSince, before, after)
	}

	// filter.Process must equal the executable name from ResolveExecutable.
	if capturedFilter.Process != exeName {
		t.Errorf("filter.Process = %q; want %s (resolved by ResolveExecutable)", capturedFilter.Process, exeName)
	}

	// The returned content must be a non-error JSON array containing the log lines.
	text := resultText(t, &r)
	var lines []map[string]any
	if err := json.Unmarshal([]byte(text), &lines); err != nil {
		t.Fatalf("response body is not a JSON array: %v; body=%s", err, text)
	}
	if len(lines) != 2 {
		t.Errorf("got %d lines; want 2", len(lines))
	}
	if !strings.Contains(text, "app started") {
		t.Errorf("missing first log line; body=%s", text)
	}
	if !strings.Contains(text, "frame rendered") {
		t.Errorf("missing second log line; body=%s", text)
	}
}

// TestLaunchThenLogs_SecondLaunchWins is the headline regression case:
// launch_app called twice on the same device+bundle — the second timestamp
// must win. logs(since=launch) must use the second timestamp, not the first.
func TestLaunchThenLogs_SecondLaunchWins(t *testing.T) {
	const bundleID = "com.squz.multimaze2"
	const exeName = "MultiMaze2"

	var capturedSince time.Time

	ios := &stubAdapter{
		resolveExecutable: func(id, bundle string) (string, bool, error) {
			return exeName, true, nil
		},
		launchApp: func(id, bundle string, env map[string]string) error { return nil },
		logRange: func(id string, filter device.LogFilter, since, until time.Time) ([]device.LogLine, error) {
			capturedSince = since
			return nil, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)

	// First launch.
	r := dispatchJSON(t, h, "launch_app", map[string]any{
		"device":    "iPad",
		"bundle_id": bundleID,
	})
	if r.IsError {
		t.Fatalf("first launch_app should succeed; body=%s", resultText(t, &r))
	}

	// Wait 50ms so the two timestamps are distinct.
	time.Sleep(50 * time.Millisecond)

	// Second launch — bracket with wall-clock bounds.
	before := time.Now()
	r = dispatchJSON(t, h, "launch_app", map[string]any{
		"device":    "iPad",
		"bundle_id": bundleID,
	})
	after := time.Now()
	if r.IsError {
		t.Fatalf("second launch_app should succeed; body=%s", resultText(t, &r))
	}

	// logs(since=launch) must use the second (newer) timestamp.
	r = dispatchJSON(t, h, "logs", map[string]any{
		"device":    "iPad",
		"bundle_id": bundleID,
		"since":     "launch",
	})
	if r.IsError {
		t.Fatalf("logs(since=launch) after second launch should succeed; body=%s", resultText(t, &r))
	}

	if capturedSince.Before(before) || capturedSince.After(after) {
		t.Errorf("since = %v; want between %v and %v (the second launch_app window — not the first)",
			capturedSince, before, after)
	}
}

// TestLaunchThenLogs_SinceLaunchNoBundleID confirms that since=launch without
// bundle_id returns an error mentioning bundle_id.
// (This mirrors coverage in logs_test.go but is included here for workflow
// completeness — skipped if already covered.)
func TestLaunchThenLogs_SinceLaunchNoBundleID(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device": "iPad",
		"since":  "launch",
		// bundle_id deliberately omitted
	})
	if !r.IsError {
		t.Fatalf("expected isError=true for since=launch without bundle_id; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "bundle_id") {
		t.Errorf("error should mention bundle_id; body=%s", resultText(t, &r))
	}
}

// TestLaunchThenLogs_SinceLaunchNoRecord confirms that since=launch with
// bundle_id but no preceding launch_app returns an error mentioning
// "no launch_app call recorded".
func TestLaunchThenLogs_SinceLaunchNoRecord(t *testing.T) {
	const bundleID = "com.squz.multimaze2"

	ios := &stubAdapter{
		resolveExecutable: func(id, bundle string) (string, bool, error) {
			return "MultiMaze2", true, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)

	// No launch_app call — since=launch must fail.
	r := dispatchJSON(t, h, "logs", map[string]any{
		"device":    "iPad",
		"bundle_id": bundleID,
		"since":     "launch",
	})
	if !r.IsError {
		t.Fatalf("expected isError=true for since=launch with no prior launch_app; body=%s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "no launch_app call recorded") {
		t.Errorf("error should mention 'no launch_app call recorded'; body=%s", resultText(t, &r))
	}
}
