// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package logcapture_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/logcapture"
)

// TestSessionAgainstRealIOS_Live wires a logcapture.Manager against a
// real iOS adapter and walks the start → wait → get → stop lifecycle.
// Gated on SPYDER_LIVE_UDID (consistent with the other iOS live tests).
//
// Asserts the headline contract: a capture started against a healthy
// device populates its buffer with log lines within a few seconds of
// any live activity, and Stop returns those lines cleanly.
func TestSessionAgainstRealIOS_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live iOS T60 test")
	}
	adapter := device.NewIOSAdapter()
	exerciseSessionLifecycle(t, adapter, udid, device.LogFilter{})
}

// TestSessionAgainstRealAndroid_Live mirrors the iOS test against the
// Android adapter. Gated on SPYDER_LIVE_ANDROID_SERIAL.
func TestSessionAgainstRealAndroid_Live(t *testing.T) {
	serial := os.Getenv("SPYDER_LIVE_ANDROID_SERIAL")
	if serial == "" {
		t.Skip("SPYDER_LIVE_ANDROID_SERIAL not set; skipping live Android T60 test")
	}
	adapter := device.NewAndroidAdapter()
	exerciseSessionLifecycle(t, adapter, serial, device.LogFilter{})
}

// TestSessionList_Live confirms Manager.List surfaces a live session
// and clears it after Stop. Runs against whichever device is configured
// (iOS preferred when both env vars are set).
func TestSessionList_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	var adapter logcapture.Adapter
	var devID string
	switch {
	case udid != "":
		adapter = device.NewIOSAdapter()
		devID = udid
	case os.Getenv("SPYDER_LIVE_ANDROID_SERIAL") != "":
		adapter = device.NewAndroidAdapter()
		devID = os.Getenv("SPYDER_LIVE_ANDROID_SERIAL")
	default:
		t.Skip("neither SPYDER_LIVE_UDID nor SPYDER_LIVE_ANDROID_SERIAL set; skipping")
	}

	mgr := logcapture.NewManager()
	defer mgr.Close()

	sess, err := mgr.Start(context.Background(), adapter, logcapture.StartParams{
		Device:   devID,
		DeviceID: devID,
		Owner:    "live-test",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	infos := mgr.List()
	found := false
	for _, info := range infos {
		if info.SessionID == sess.ID {
			found = true
			if info.Owner != "live-test" {
				t.Errorf("List entry owner = %q; want %q", info.Owner, "live-test")
			}
		}
	}
	if !found {
		t.Errorf("List did not include just-started session %s; got %d entries", sess.ID, len(infos))
	}
	if _, err := mgr.Stop(sess.ID); err != nil {
		t.Errorf("Stop: %v", err)
	}
	for _, info := range mgr.List() {
		if info.SessionID == sess.ID {
			t.Errorf("List still includes session %s after Stop", sess.ID)
		}
	}
}

// exerciseSessionLifecycle runs the common start → wait → get → stop
// shape against any adapter/device pair, asserting that:
//   - the buffer accumulates lines (Get returns >0)
//   - capture continues after Get (a second sample also sees lines)
//   - Stop drains and tears down cleanly
//   - a second Stop on the same id errors
//
// Tolerates a small initial settling window for the underlying tap to
// open and start delivering.
func exerciseSessionLifecycle(t *testing.T, adapter logcapture.Adapter, devID string, filter device.LogFilter) {
	t.Helper()
	mgr := logcapture.NewManager()
	defer mgr.Close()

	sess, err := mgr.Start(context.Background(), adapter, logcapture.StartParams{
		Device:   devID,
		DeviceID: devID,
		Filter:   filter,
		Owner:    "live-test",
		TTL:      2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _, _ = mgr.Stop(sess.ID) }()

	// Settle: wait for the underlying tap to open and lines to start
	// arriving. 4 s is generous for both iOS DTX handshake (~200 ms)
	// and Android logcat (~50 ms) given the per-platform buffering.
	time.Sleep(4 * time.Second)

	first, err := mgr.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	if len(first.Lines) == 0 {
		t.Fatalf("Get #1 returned 0 lines on a live device after a 4 s settle window; tap not delivering")
	}
	t.Logf("Get #1: %d lines, dropped=%d (sample: %q)", len(first.Lines), first.DroppedLines, sampleMessage(first.Lines))

	// Capture should resume after Get clears the buffer.
	time.Sleep(2 * time.Second)
	second, err := mgr.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if len(second.Lines) == 0 {
		t.Errorf("Get #2 returned 0 lines after a 2 s post-drain window; capture appears to have stopped after Get")
	}

	stop, err := mgr.Stop(sess.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	t.Logf("Stop: %d lines remaining, dropped=%d total since last Get",
		len(stop.Lines), stop.DroppedLines)

	if _, err := mgr.Stop(sess.ID); err == nil {
		t.Error("Stop on already-stopped session: want error, got nil")
	}
	if _, err := mgr.Get(sess.ID); err == nil {
		t.Error("Get on stopped session: want error, got nil")
	}
}

func sampleMessage(lines []device.LogLine) string {
	if len(lines) == 0 {
		return ""
	}
	m := lines[0].Message
	if len(m) > 80 {
		return m[:80] + "…"
	}
	return m
}
