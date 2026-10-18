// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package logcapture

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/paths"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "spyder-logcapture")
	if err != nil {
		panic(err)
	}
	os.Setenv(paths.DataDirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestFreshAnchorUsesSequenceNotWallClock(t *testing.T) {
	m := NewManager()
	defer m.Close()
	now := time.Now()
	ad := &fakeAdapter{lines: []device.LogLine{
		{Timestamp: now, Level: "info", Tag: "Other", Message: "noise"},
		{Timestamp: now.Add(-2 * time.Hour), Level: "info", Tag: "StockCarsRegression", Message: "nonce"},
	}}
	s, err := m.Start(context.Background(), ad, StartParams{
		Device: "phone", DeviceID: "serial-1", Platform: "android",
		Marker: "nonce", MarkerTag: "StockCarsRegression",
		MaxBytes: 1024, MaxLines: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		got, _ := s.snapshot()
		return len(got) >= 2
	})
	res, err := m.Stop(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Integrity.FreshAnchor == nil || res.Integrity.FreshAnchor.Seq != 2 {
		t.Fatalf("anchor = %+v, want seq 2", res.Integrity.FreshAnchor)
	}
	if res.Integrity.Ordering != "seq" {
		t.Fatalf("ordering = %s", res.Integrity.Ordering)
	}
	if len(res.Lines) != 2 || res.Lines[1].Diagnostic == nil || res.Lines[1].Diagnostic.Seq != 2 {
		t.Fatalf("lines not sequenced")
	}
	if res.Lines[1].Timestamp.After(res.Lines[0].Timestamp) {
		t.Fatal("fixture wall clocks were not inverted")
	}
	raw, err := os.ReadFile(res.Integrity.DurablePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("durable capture is empty")
	}
	if !res.Integrity.ReaderReaped || !res.Integrity.Cancelled {
		t.Fatalf("stop did not reap the reader: %+v", res.Integrity)
	}
	if len(res.Integrity.UnsupportedCrashChannels) == 0 {
		t.Fatal("unsupported crash channels were omitted")
	}
}

func TestDurableLimitIsExplicit(t *testing.T) {
	m := NewManager()
	defer m.Close()
	ad := &fakeAdapter{lines: []device.LogLine{
		{Message: "0123456789"},
		{Message: "abcdefghijklmnopqrstuvwxyz"},
	}}
	s, err := m.Start(context.Background(), ad, StartParams{
		DeviceID: "serial-2", MaxBytes: 16, MaxLines: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.durableTruncated
	})
	res, err := m.Stop(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Integrity.DurableTruncated || !res.Integrity.Incomplete || res.Integrity.DurableDropped == 0 {
		t.Fatalf("truncation was not explicit: %+v", res.Integrity)
	}
}

func TestCancelOneCaptureLeavesTheOther(t *testing.T) {
	m := NewManager()
	defer m.Close()
	slow := &fakeAdapter{lines: []device.LogLine{{Message: "a"}, {Message: "b"}}, lineDelay: 30 * time.Millisecond}
	other := &fakeAdapter{lines: []device.LogLine{{Message: "kept"}}, lineDelay: 30 * time.Millisecond}
	s1, err := m.Start(context.Background(), slow, StartParams{DeviceID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := m.Start(context.Background(), other, StartParams{DeviceID: "two"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		got, _ := s2.snapshot()
		return len(got) >= 1
	})
	if _, err := m.Stop(s1.ID); err != nil {
		t.Fatal(err)
	}
	res, err := m.Get(s2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Integrity.Cancelled || res.Integrity.ReaderReaped {
		t.Fatalf("other device capture was disturbed: %+v", res.Integrity)
	}
	if _, err := m.Stop(s2.ID); err != nil {
		t.Fatal(err)
	}
}
