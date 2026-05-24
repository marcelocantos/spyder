// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"testing"
	"time"
)

// TestRunMonitor_ExitsOnContextCancel asserts the monitor lifecycle
// is clean — cancelling the context returns RunMonitor without
// leaking goroutines or blocking on the log-tail subprocess.
func TestRunMonitor_ExitsOnContextCancel(t *testing.T) {
	savePoll, saveDeb := pollInterval, debounceDelay
	pollInterval = 50 * time.Millisecond
	debounceDelay = 10 * time.Millisecond
	t.Cleanup(func() {
		pollInterval = savePoll
		debounceDelay = saveDeb
	})

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		RunMonitor(ctx)
		close(finished)
	}()

	// Let the monitor tick a few times — exercises the timer path
	// and the immediate startup check.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("RunMonitor did not return after context cancel")
	}
}

// TestReconcile_SingleAttemptPerEpisode asserts the 🎯T72.5 contract:
// detection + snapshot run on every wedged reconcile, but auto-recovery
// fires at most once per continuous wedge episode (no 2-minute churn). A
// fresh episode after recovery re-arms one more attempt.
func TestReconcile_SingleAttemptPerEpisode(t *testing.T) {
	var wedged bool
	var snapshots, recoveries int

	saveW, saveC, saveR := isWedgedFn, captureFn, recoverFn
	t.Cleanup(func() { isWedgedFn, captureFn, recoverFn = saveW, saveC, saveR })

	isWedgedFn = func() (bool, int, int, error) { return wedged, 1, 0, nil }
	captureFn = func(_, _ string) { snapshots++ }
	recoverFn = func(_ context.Context) error { recoveries++; return nil }

	ctx := context.Background()
	var st wedgeState

	// Episode 1: first detection fires recovery once...
	wedged = true
	reconcile(ctx, "t1", &st)
	// ...subsequent detections in the same episode snapshot but do NOT
	// re-fire recovery ("detect, snapshot, no auto-recovery").
	reconcile(ctx, "t2", &st)
	reconcile(ctx, "t3", &st)
	if recoveries != 1 {
		t.Errorf("episode 1 recoveries = %d; want exactly 1 (no churn)", recoveries)
	}
	if snapshots != 3 {
		t.Errorf("snapshots = %d; want 3 (one per wedged reconcile)", snapshots)
	}

	// Episode ends — a healthy reconcile clears state without snapshot/recovery.
	wedged = false
	reconcile(ctx, "clear", &st)
	if snapshots != 3 || recoveries != 1 {
		t.Errorf("healthy reconcile should be a no-op: snapshots=%d recoveries=%d", snapshots, recoveries)
	}

	// Episode 2: a fresh wedge re-arms one attempt.
	wedged = true
	reconcile(ctx, "t4", &st)
	if recoveries != 2 {
		t.Errorf("episode 2 recoveries total = %d; want 2 (one per episode)", recoveries)
	}
}
