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

	// Stub the reconcile seams: without this, reconcile calls the REAL
	// IsWedged/AttemptRecovery, which shells out (ioreg, sudo
	// spyder-killusbmuxd) with unbounded real-world latency. Under full
	// -parallel/-race load that real exec can outlast this test's 2s
	// deadline, and it has no business running as a side effect of a
	// pure context-cancel lifecycle test in the first place.
	saveW, saveC, saveR := isWedgedFn, captureFn, recoverFn
	isWedgedFn = func() (bool, int, int, error) { return false, 0, 0, nil }
	captureFn = func(_, _ string) {}
	recoverFn = func(_ context.Context) error { return nil }
	t.Cleanup(func() { isWedgedFn, captureFn, recoverFn = saveW, saveC, saveR })

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

// stubReconcile replaces reconcile's seams with a controllable clock and
// device view.
func stubReconcile(t *testing.T, wedged *bool, missing *[]string) (now *time.Time, snapshots, recoveries *int) {
	t.Helper()
	saveW, saveC, saveR, saveM, saveN := isWedgedFn, captureFn, recoverFn, missingFn, nowFn
	t.Cleanup(func() { isWedgedFn, captureFn, recoverFn, missingFn, nowFn = saveW, saveC, saveR, saveM, saveN })
	clock := time.Date(2026, 11, 23, 8, 35, 0, 0, time.UTC)
	var snaps, recs int
	isWedgedFn = func() (bool, int, int, error) { return *wedged, 3, 2, nil }
	captureFn = func(_, _ string) { snaps++ }
	recoverFn = func(_ context.Context) error { recs++; return nil }
	missingFn = func() ([]string, error) { return *missing, nil }
	nowFn = func() time.Time { return clock }
	return &clock, &snaps, &recs
}

// TestReconcile_SingleAttemptPerEpisode asserts the 🎯T72.5 contract:
// auto-recovery fires at most once per continuous, confirmed wedge
// episode (no 2-minute churn). A fresh episode re-arms one more attempt.
func TestReconcile_SingleAttemptPerEpisode(t *testing.T) {
	wedged, missing := true, []string{"00008130-0009702E1110001C"}
	now, _, recoveries := stubReconcile(t, &wedged, &missing)
	ctx := context.Background()
	var st wedgeState

	reconcile(ctx, "t1", &st)
	*now = now.Add(confirmWindow)
	reconcile(ctx, "t2", &st) // confirmed: recovery
	*now = now.Add(time.Minute)
	reconcile(ctx, "t3", &st)
	reconcile(ctx, "t4", &st)
	if *recoveries != 1 {
		t.Errorf("episode 1 recoveries = %d; want exactly 1 (no churn)", *recoveries)
	}

	wedged = false
	reconcile(ctx, "clear", &st)

	wedged = true
	reconcile(ctx, "t5", &st)
	*now = now.Add(confirmWindow)
	reconcile(ctx, "t6", &st)
	if *recoveries != 2 {
		t.Errorf("episode 2 recoveries total = %d; want 2 (one per episode)", *recoveries)
	}
}

// One device detaching or being unplugged leaves iousb > usbmux for a
// few seconds. That must never kill usbmuxd, which drops every other
// iOS device's tunnel and XCTest session (2026-11-17 18:54, -18 19:25,
// -23 08:35: each kill came 1–5 s after a detach).
func TestReconcile_TransientMismatchNeverKills(t *testing.T) {
	wedged, missing := true, []string{"00008130-0009702E1110001C"}
	now, _, recoveries := stubReconcile(t, &wedged, &missing)
	ctx := context.Background()
	var st wedgeState

	reconcile(ctx, "log-tail", &st) // 5 s after the detach
	*now = now.Add(1 * time.Second)
	reconcile(ctx, "poll", &st)
	*now = now.Add(10 * time.Second)
	wedged = false // IOKit caught up
	reconcile(ctx, "poll", &st)
	if *recoveries != 0 {
		t.Fatalf("transient mismatch killed usbmuxd %d time(s)", *recoveries)
	}
	// A later, separate blip starts its own clock.
	wedged = true
	reconcile(ctx, "poll", &st)
	*now = now.Add(confirmWindow - time.Second)
	reconcile(ctx, "poll", &st)
	if *recoveries != 0 {
		t.Fatalf("mismatch younger than confirmWindow killed usbmuxd")
	}
}

// A mismatch that persists but names no device usbmux has lost (the
// 2026-11-18 snapshot said wedged=false missing_from_usbmux=[]) is not
// corroborated and does not kill usbmuxd; once a device is missing, it does.
func TestReconcile_PersistentMismatchNeedsAMissingDevice(t *testing.T) {
	wedged := true
	var missing []string
	now, _, recoveries := stubReconcile(t, &wedged, &missing)
	ctx := context.Background()
	var st wedgeState

	reconcile(ctx, "poll", &st)
	*now = now.Add(2 * confirmWindow)
	reconcile(ctx, "poll", &st)
	if *recoveries != 0 {
		t.Fatalf("uncorroborated mismatch killed usbmuxd")
	}
	missing = []string{"00008110-0014182E0AC2801E"}
	*now = now.Add(30 * time.Second)
	reconcile(ctx, "poll", &st)
	if *recoveries != 1 {
		t.Fatalf("corroborated, persistent wedge recoveries = %d; want 1", *recoveries)
	}
}
