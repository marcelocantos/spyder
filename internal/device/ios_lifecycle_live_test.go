// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBouncingBallBundleID is the bundle id of the test fixture
// app at ios/BouncingBall/. Built + signed via xcodebuild and
// installed manually on every reference iOS device; emits a steady
// stream of os_log entries on every wall bounce so the log-capture
// tests have a guaranteed emitter. Override via SPYDER_LIVE_BUNDLE_ID
// when an alternative fixture is needed on a specific device.
const TestBouncingBallBundleID = "com.marcelocantos.spyder.BouncingBall"

// liveIOSLaunchBundle returns the bundle id to use as the
// launch/terminate target. Prefers SPYDER_LIVE_BUNDLE_ID (caller
// override), falling back to BouncingBall. Skips the test when
// neither is installed, since launching arbitrary other apps risks
// destabilising the device (we observed Jevons disconnecting when
// jevon's expired profile was repeatedly poked).
func liveIOSLaunchBundle(t *testing.T, a *IOSAdapter, udid string) string {
	t.Helper()
	bundleID := os.Getenv("SPYDER_LIVE_BUNDLE_ID")
	if bundleID == "" {
		bundleID = TestBouncingBallBundleID
	}
	_, installed, err := a.ResolveExecutable(udid, bundleID)
	if err != nil {
		t.Fatalf("ResolveExecutable(%s): %v", bundleID, err)
	}
	if !installed {
		t.Skipf("test fixture %s not installed on %s — build ios/BouncingBall and install via `xcrun devicectl device install app` (or set SPYDER_LIVE_BUNDLE_ID to a known-good already-installed app)", bundleID, udid)
	}
	return bundleID
}

// TestIOSLaunchTerminateCycle_Live walks the LaunchApp → AppPID →
// TerminateApp lifecycle against the BouncingBall fixture (or a
// caller-specified bundle via SPYDER_LIVE_BUNDLE_ID). Contract:
// launch yields a pid, terminate clears it.
func TestIOSLaunchTerminateCycle_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live iOS test")
	}
	a := NewIOSAdapter()
	bundleID := liveIOSLaunchBundle(t, a, udid)

	// Cleanup before the test.
	_ = a.TerminateApp(udid, bundleID)
	time.Sleep(500 * time.Millisecond)

	if err := a.LaunchApp(udid, bundleID, nil); err != nil {
		if !strings.Contains(err.Error(), "pidFromResponse") {
			t.Fatalf("LaunchApp(%s, %s): %v", udid, bundleID, err)
		}
		t.Logf("LaunchApp returned upstream pidFromResponse quirk; will verify via AppPID")
	}

	var pid int
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, err := a.AppPID(udid, bundleID)
		if err == nil && p > 0 {
			pid = p
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatalf("AppPID(%s, %s) didn't resolve within 8s of launch", udid, bundleID)
	}
	t.Logf("LaunchApp(%s, %s) → pid=%d", udid, bundleID, pid)

	if err := a.TerminateApp(udid, bundleID); err != nil {
		t.Errorf("TerminateApp(%s, %s): %v", udid, bundleID, err)
	}
	gone := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, err := a.AppPID(udid, bundleID)
		if err != nil && strings.Contains(err.Error(), "not running") {
			gone = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !gone {
		t.Errorf("AppPID(%s, %s) still resolves after TerminateApp; teardown failed", udid, bundleID)
	}
}

// TestIOSLogStream_Live drains LogStream for 3 seconds and asserts at
// least one line arrives. Complements TestLogRange_Live which uses the
// bounded query path.
func TestIOSLogStream_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live iOS test")
	}
	a := NewIOSAdapter()
	out := make(chan LogLine, 256)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.LogStream(ctx, udid, LogFilter{}, out) }()
	var got int
	deadline := time.After(4 * time.Second)
loop:
	for {
		select {
		case <-out:
			got++
		case <-deadline:
			break loop
		}
	}
	if got == 0 {
		t.Errorf("LogStream(%s) drained 0 lines over ~3s; expected continuous syslog traffic", udid)
	} else {
		t.Logf("LogStream(%s): %d lines drained over ~3s", udid, got)
	}
	if err := <-errCh; err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("LogStream(%s) returned err=%v; want nil, DeadlineExceeded, or Canceled", udid, err)
	}
}

// TestIOSCrashes_Live asserts that Crashes returns a slice (possibly
// empty) and never errors on a healthy device. The afc-over-crash
// service is always available on a paired developer device.
func TestIOSCrashes_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live iOS test")
	}
	a := NewIOSAdapter()
	reports, err := a.Crashes(udid, time.Now().Add(-30*24*time.Hour), "")
	if err != nil {
		t.Fatalf("Crashes(%s): %v", udid, err)
	}
	t.Logf("Crashes(%s, last 30d): %d report(s)", udid, len(reports))
	for i, r := range reports[:min(3, len(reports))] {
		t.Logf("  [%d] process=%s reason=%q ts=%s", i, r.Process, r.Reason, r.Timestamp.Format(time.RFC3339))
	}
}
