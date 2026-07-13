// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDesktopAdapter_LaunchLifecycle exercises the launch/lifecycle surface
// (🎯T85) with a trivial script standing in for a game: launch → AppPID
// reports running → stdout is captured into LogRange → TerminateApp stops it
// and AppPID reports not-running.
func TestDesktopAdapter_LaunchLifecycle(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-game.sh")
	// Emit a line (to test capture), then block so the process stays alive.
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ready\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := NewDesktopAdapter(nil)
	const bundle = "com.test.fake"
	if err := a.LaunchApp(script, bundle, map[string]string{"SPYDER_APP_CHANNEL": "127.0.0.1:1"}); err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	t.Cleanup(func() { _ = a.TerminateApp(script, bundle) })

	pid, err := a.AppPID(script, bundle)
	if err != nil || pid <= 0 {
		t.Fatalf("AppPID after launch: pid=%d err=%v", pid, err)
	}

	// Captured stdout must reach LogRange.
	if !eventually(t, 2*time.Second, func() bool {
		lines, _ := a.LogRange(script, LogFilter{}, time.Time{}, time.Time{})
		for _, ll := range lines {
			if strings.Contains(ll.Message, "ready") {
				return true
			}
		}
		return false
	}) {
		t.Fatal("stdout line 'ready' was not captured into LogRange")
	}

	if err := a.TerminateApp(script, bundle); err != nil {
		t.Fatalf("TerminateApp: %v", err)
	}
	if !eventually(t, 3*time.Second, func() bool {
		_, err := a.AppPID(script, bundle)
		return err != nil // not-running is reported as an error
	}) {
		t.Fatal("process still reported running after TerminateApp")
	}
}

// TestDesktopAdapter_UnsupportedAreClean verifies the not-on-desktop surface
// returns clear errors rather than panicking (🎯T85).
func TestDesktopAdapter_UnsupportedAreClean(t *testing.T) {
	a := NewDesktopAdapter(nil)
	if _, err := a.Screenshot("x"); err == nil {
		t.Error("Screenshot should error on desktop")
	}
	if err := a.Rotate("x", "portrait"); err == nil {
		t.Error("Rotate should error on desktop")
	}
	if err := a.InstallApp("x", "y"); err == nil {
		t.Error("InstallApp should error on desktop")
	}
	// ListApps is a benign empty (not an error) so log/state probes don't fail.
	if apps, err := a.ListApps("x"); err != nil || len(apps) != 0 {
		t.Errorf("ListApps: want empty/no-error, got %v / %v", apps, err)
	}
}

func eventually(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
