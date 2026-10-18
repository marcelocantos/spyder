// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDiagnosticAndroidFreshLog_Live starts captures on two Android
// devices, emits a fresh log after each capture is up, and checks the
// schema, separate clocks, and that cancelling one reader does not reap
// the other. Gated on SPYDER_LIVE_ANDROID_SERIAL and
// SPYDER_LIVE_ANDROID_OTHER.
func TestDiagnosticAndroidFreshLog_Live(t *testing.T) {
	serial := os.Getenv("SPYDER_LIVE_ANDROID_SERIAL")
	other := os.Getenv("SPYDER_LIVE_ANDROID_OTHER")
	if serial == "" || other == "" {
		t.Skip("SPYDER_LIVE_ANDROID_SERIAL and SPYDER_LIVE_ANDROID_OTHER are required")
	}
	adapter := NewAndroidAdapter()
	nonce := fmt.Sprintf("spyder-fresh-%d", time.Now().UnixNano())
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	outA := make(chan LogLine, 256)
	outB := make(chan LogLine, 256)
	errA := make(chan error, 1)
	errB := make(chan error, 1)
	beforeA := logcatPIDs(serial)
	beforeB := logcatPIDs(other)
	go func() { errA <- adapter.LogStream(ctxA, serial, LogFilter{}, outA) }()
	go func() { errB <- adapter.LogStream(ctxB, other, LogFilter{}, outB) }()

	line := waitForFreshLog(t, outA, serial, nonce)
	assertFreshAndroid(t, line, nonce)
	if bundle := os.Getenv("SPYDER_LIVE_BUNDLE_ID"); bundle != "" {
		if err := adapter.LaunchApp(serial, bundle, nil); err != nil {
			t.Fatalf("LaunchApp(%s): %v", bundle, err)
		}
		t.Cleanup(func() { _ = adapter.TerminateApp(serial, bundle) })
		if _, ok := readMatching(outA, 10*time.Second, func(ll LogLine) bool {
			raw := ""
			if ll.Diagnostic != nil {
				raw = ll.Diagnostic.Raw.Text
			}
			return strings.Contains(ll.Message, bundle) || strings.Contains(raw, bundle)
		}); !ok {
			t.Fatalf("no fresh log mentioning %s after launch", bundle)
		}
	}
	pidA := newLogcatPID(t, serial, beforeA)
	pidB := newLogcatPID(t, other, beforeB)
	cancelA()
	select {
	case err := <-errA:
		if err != nil {
			t.Fatalf("cancelled capture: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Android capture did not return")
	}
	if processAlive(pidA) {
		t.Fatalf("logcat for %s pid %d still running after cancel", serial, pidA)
	}
	if !processAlive(pidB) {
		t.Fatalf("cancelling %s reaped logcat %d for %s", serial, pidB, other)
	}
	nonceB := nonce + "-other"
	lineB := waitForFreshLog(t, outB, other, nonceB)
	assertFreshAndroid(t, lineB, nonceB)
	cancelB()
	select {
	case err := <-errB:
		if err != nil {
			t.Fatalf("second capture: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second Android capture did not return")
	}
}

// TestDiagnosticIOSFreshAppLog_Live launches one installed app after the
// capture starts and requires a log from that executable. A second device
// keeps streaming after the first capture is cancelled. Gated on
// SPYDER_LIVE_UDID, SPYDER_LIVE_UDID_OTHER, and SPYDER_LIVE_BUNDLE_ID.
func TestDiagnosticIOSFreshAppLog_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	other := os.Getenv("SPYDER_LIVE_UDID_OTHER")
	bundleID := os.Getenv("SPYDER_LIVE_BUNDLE_ID")
	if udid == "" || other == "" || bundleID == "" {
		t.Skip("SPYDER_LIVE_UDID, SPYDER_LIVE_UDID_OTHER, and SPYDER_LIVE_BUNDLE_ID are required")
	}
	adapter := NewIOSAdapter()
	exe, installed, err := adapter.ResolveExecutable(udid, bundleID)
	if err != nil {
		t.Fatalf("ResolveExecutable: %v", err)
	}
	if !installed || exe == "" {
		t.Fatalf("bundle %s is not installed on %s", bundleID, udid)
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	outA := make(chan LogLine, 512)
	outB := make(chan LogLine, 256)
	errA := make(chan error, 1)
	errB := make(chan error, 1)
	go func() { errA <- adapter.LogStream(ctxA, udid, LogFilter{Process: exe}, outA) }()
	go func() { errB <- adapter.LogStream(ctxB, other, LogFilter{}, outB) }()
	// Prove the other device is delivering before we cancel the first.
	if _, ok := readLog(outB, 8*time.Second); !ok {
		t.Fatal("other iOS device produced no log before cancellation")
	}
	_ = adapter.TerminateApp(udid, bundleID)
	time.Sleep(300 * time.Millisecond)
	if err := adapter.LaunchApp(udid, bundleID, nil); err != nil {
		t.Fatalf("LaunchApp(%s): %v", bundleID, err)
	}
	t.Cleanup(func() { _ = adapter.TerminateApp(udid, bundleID) })
	line, ok := readMatching(outA, 12*time.Second, func(ll LogLine) bool {
		return ll.Process == exe || (ll.Diagnostic != nil && ll.Diagnostic.Process.Value == exe)
	})
	if !ok {
		t.Fatalf("no fresh log from %s (%s) after launch", bundleID, exe)
	}
	if line.Diagnostic == nil || line.Diagnostic.SchemaVersion != DiagnosticSchemaVersion || line.Diagnostic.Platform != "ios" {
		t.Fatalf("ios record missing schema: %+v", line.Diagnostic)
	}
	if line.Diagnostic.DeviceTime.Source == line.Diagnostic.HostReceipt.Source {
		t.Fatalf("ios clocks collapsed: %+v %+v", line.Diagnostic.DeviceTime, line.Diagnostic.HostReceipt)
	}
	if line.Diagnostic.Process.Status != IdentityObserved || line.Diagnostic.ProcessGeneration.Status == IdentityObserved {
		t.Fatalf("process identity = %+v generation %+v", line.Diagnostic.Process, line.Diagnostic.ProcessGeneration)
	}
	if line.Diagnostic.Raw.Text == "" || line.Diagnostic.Parse.Status == "" {
		t.Fatalf("raw evidence missing: %+v", line.Diagnostic)
	}
	cancelA()
	select {
	case err := <-errA:
		if err != nil {
			t.Fatalf("cancelled ios capture: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled iOS capture did not return")
	}
	if _, ok := readLog(outB, 8*time.Second); !ok {
		t.Fatal("other iOS device stopped delivering after the first capture was cancelled")
	}
	cancelB()
	select {
	case err := <-errB:
		if err != nil {
			t.Fatalf("second ios capture: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("second iOS capture did not return")
	}
}

func waitForFreshLog(t *testing.T, out <-chan LogLine, serial, nonce string) LogLine {
	t.Helper()
	// Give logcat a moment to attach before the fresh line, then retry
	// the write until the capture sees it.
	deadline := time.Now().Add(12 * time.Second)
	emitted := false
	for time.Now().Before(deadline) {
		if !emitted || time.Now().After(deadline.Add(-9*time.Second)) {
			cmd := exec.Command("adb", "-s", serial, "shell", "log", "-p", "I", "-t", "SpyderFresh", nonce)
			if err := cmd.Run(); err != nil && !emitted {
				t.Fatalf("adb log on %s: %v", serial, err)
			}
			emitted = true
		}
		if ll, ok := readMatching(out, 1500*time.Millisecond, func(ll LogLine) bool {
			return ll.Message == nonce || (ll.Diagnostic != nil && ll.Diagnostic.Message == nonce)
		}); ok {
			return ll
		}
	}
	t.Fatalf("fresh log %q not observed on %s", nonce, serial)
	return LogLine{}
}

func assertFreshAndroid(t *testing.T, ll LogLine, nonce string) {
	t.Helper()
	d := ll.Diagnostic
	if d == nil || d.SchemaVersion != DiagnosticSchemaVersion || d.Platform != "android" {
		t.Fatalf("android record missing schema: %+v", d)
	}
	if d.Message != nonce || d.Meta["tag"] != "SpyderFresh" || d.Severity != "info" {
		t.Fatalf("fresh record = tag %s sev %s msg %q", d.Meta["tag"], d.Severity, d.Message)
	}
	if !strings.Contains(d.Raw.Text, nonce) || d.Raw.Format == "" {
		t.Fatalf("raw evidence missing: %+v", d.Raw)
	}
	if d.DeviceTime.Source == d.HostReceipt.Source || d.HostReceipt.Source != "host_receipt" {
		t.Fatalf("clocks = device %+v host %+v", d.DeviceTime, d.HostReceipt)
	}
	if d.PIDStatus != IdentityObserved || d.App.Status == IdentityObserved {
		t.Fatalf("pid %s app %+v", d.PIDStatus, d.App)
	}
	if d.ProcessGeneration.Status != IdentityObserved && d.ProcessGeneration.Status != IdentityUnverified {
		t.Fatalf("process generation was not explicit: %+v", d.ProcessGeneration)
	}
	if d.Seq == 0 {
		t.Fatal("capture sequence was not assigned")
	}
}

func readLog(out <-chan LogLine, d time.Duration) (LogLine, bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case ll := <-out:
		return ll, true
	case <-timer.C:
		return LogLine{}, false
	}
}

func readMatching(out <-chan LogLine, d time.Duration, match func(LogLine) bool) (LogLine, bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ll, ok := readLog(out, time.Until(deadline))
		if !ok {
			return LogLine{}, false
		}
		if match(ll) {
			return ll, true
		}
	}
	return LogLine{}, false
}

func logcatPIDs(serial string) map[int]struct{} {
	out, err := exec.Command("pgrep", "-f", "adb -s "+serial+" logcat").Output()
	set := map[int]struct{}{}
	if err != nil {
		return set
	}
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err == nil && pid > 0 {
			set[pid] = struct{}{}
		}
	}
	return set
}

func newLogcatPID(t *testing.T, serial string, before map[int]struct{}) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for pid := range logcatPIDs(serial) {
			if _, old := before[pid]; !old {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no new logcat process for %s", serial)
	return 0
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
