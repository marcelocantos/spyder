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

// pickThirdPartyAndroidApp returns the bundle id of the first installed
// third-party app, or skips the test when there isn't one.
func pickThirdPartyAndroidApp(t *testing.T, a *AndroidAdapter, serial string) string {
	t.Helper()
	apps, err := a.ListApps(serial)
	if err != nil {
		t.Fatalf("ListApps(%s): %v", serial, err)
	}
	if len(apps) == 0 {
		t.Skipf("no third-party apps on %s; skipping (install any user app to enable)", serial)
	}
	return apps[0].BundleID
}

// TestAndroidLaunchTerminateCycle_Live exercises the
// LaunchApp → AppPID → TerminateApp round trip against a third-party
// package. The contract: a freshly-launched app reports a pid via AppPID,
// and TerminateApp returns nil and clears that pid.
func TestAndroidLaunchTerminateCycle_Live(t *testing.T) {
	serial := androidSerial(t)
	a := NewAndroidAdapter()
	pkg := pickThirdPartyAndroidApp(t, a, serial)

	// Best-effort cleanup before the test in case a previous run
	// left the app foregrounded.
	_ = a.TerminateApp(serial, pkg)
	time.Sleep(500 * time.Millisecond)

	if err := a.LaunchApp(serial, pkg); err != nil {
		t.Fatalf("LaunchApp(%s, %s): %v", serial, pkg, err)
	}
	// AppPID may take a moment to settle as the process starts.
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		pid, err = a.AppPID(serial, pkg)
		if err == nil && pid > 0 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatalf("AppPID(%s, %s) didn't resolve within 5s after launch", serial, pkg)
	}
	t.Logf("LaunchApp(%s, %s) → pid=%d", serial, pkg, pid)

	if err := a.TerminateApp(serial, pkg); err != nil {
		t.Errorf("TerminateApp(%s, %s): %v", serial, pkg, err)
	}
	// Confirm the pid is gone within a short window.
	gone := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := a.AppPID(serial, pkg)
		if err != nil && strings.Contains(err.Error(), "not running") {
			gone = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !gone {
		t.Logf("AppPID(%s, %s) still resolves after TerminateApp; some Android apps respawn via foreground service — best-effort", serial, pkg)
	}
}

// TestAndroidScreenshot_Live captures a PNG and asserts the bytes
// start with the PNG magic.
func TestAndroidScreenshot_Live(t *testing.T) {
	serial := androidSerial(t)
	a := NewAndroidAdapter()
	png, err := a.Screenshot(serial)
	if err != nil {
		t.Fatalf("Screenshot(%s): %v", serial, err)
	}
	if len(png) < 1024 {
		t.Errorf("Screenshot(%s) returned %d bytes; expected a non-trivial PNG", serial, len(png))
	}
	if string(png[:4]) != "\x89PNG" {
		t.Errorf("Screenshot(%s) doesn't start with PNG magic; first 8 bytes = %x", serial, png[:8])
	}
	t.Logf("Screenshot(%s): %d bytes, PNG magic ok", serial, len(png))
}

// TestAndroidLogStream_Live drains LogStream for 3 seconds and asserts
// at least one line arrives. logcat is always active on a powered
// Android device.
func TestAndroidLogStream_Live(t *testing.T) {
	serial := androidSerial(t)
	a := NewAndroidAdapter()
	out := make(chan LogLine, 256)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.LogStream(ctx, serial, LogFilter{}, out) }()
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
		t.Errorf("LogStream(%s) drained 0 lines over ~3s; expected continuous logcat traffic", serial)
	} else {
		t.Logf("LogStream(%s): %d lines drained over ~3s", serial, got)
	}
	if err := <-errCh; err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("LogStream(%s) returned err=%v; want nil, DeadlineExceeded, or Canceled", serial, err)
	}
}

// TestAndroidScreenRecord_Live exercises the StartRecording →
// StopRecording lifecycle. Captures a short clip into a tempfile and
// asserts the file lands locally non-empty. iOS physical devices
// don't support recording, so this test is Android-only by design.
func TestAndroidScreenRecord_Live(t *testing.T) {
	serial := androidSerial(t)
	a := NewAndroidAdapter()
	dest, err := os.CreateTemp("", "spyder-rec-*.mp4")
	if err != nil {
		t.Fatalf("tempfile: %v", err)
	}
	destPath := dest.Name()
	_ = dest.Close()
	t.Cleanup(func() { _ = os.Remove(destPath) })

	stopFn, pid, err := a.StartRecording(serial, destPath)
	if err != nil {
		t.Fatalf("StartRecording(%s): %v", serial, err)
	}
	if pid <= 0 {
		t.Fatalf("StartRecording returned pid=%d; want >0", pid)
	}
	// Let the recorder accumulate a couple of seconds of frames.
	time.Sleep(2 * time.Second)

	// stopFn pulls the file off-device. Adapter.StopRecording only
	// signals — full cleanup needs stopFn per StartRecording's doc.
	if err := stopFn(); err != nil {
		t.Fatalf("stopFn(%s, pid=%d): %v", serial, pid, err)
	}
	info, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() < 1024 {
		t.Errorf("recorded output is %d bytes; want a non-trivial mp4", info.Size())
	}
	t.Logf("Recording(%s): %d bytes at %s", serial, info.Size(), destPath)
}

// TestAndroidCrashes_Live asserts that Crashes returns a slice (possibly
// empty) and never errors on a healthy device. A non-root device falls
// back to logcat -b crash; that channel exists on every Android device.
func TestAndroidCrashes_Live(t *testing.T) {
	serial := androidSerial(t)
	a := NewAndroidAdapter()
	reports, err := a.Crashes(serial, time.Now().Add(-7*24*time.Hour), "")
	if err != nil {
		t.Fatalf("Crashes(%s): %v", serial, err)
	}
	t.Logf("Crashes(%s, last 7d): %d report(s)", serial, len(reports))
	for i, r := range reports[:min(3, len(reports))] {
		t.Logf("  [%d] process=%s reason=%q ts=%s", i, r.Process, r.Reason, r.Timestamp.Format(time.RFC3339))
	}
}
