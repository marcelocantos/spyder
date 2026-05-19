// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package logcapture

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// fakeAdapter feeds a scripted slice of LogLines into the channel and
// then waits for ctx to fire. lineDelay introduces a small interval
// between sends so tests can interleave with manager operations.
type fakeAdapter struct {
	lines     []device.LogLine
	lineDelay time.Duration

	mu       sync.Mutex
	calls    int
	lastID   string
	lastFilt device.LogFilter
}

func (f *fakeAdapter) LogStream(ctx context.Context, id string, filter device.LogFilter, out chan<- device.LogLine) error {
	f.mu.Lock()
	f.calls++
	f.lastID = id
	f.lastFilt = filter
	f.mu.Unlock()
	for _, ll := range f.lines {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- ll:
		}
		if f.lineDelay > 0 {
			time.Sleep(f.lineDelay)
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func mkLine(i int, msg string) device.LogLine {
	return device.LogLine{
		Timestamp: time.Now(),
		Process:   "TestApp",
		Level:     "default",
		Message:   fmt.Sprintf("[%d] %s", i, msg),
	}
}

func TestManager_StartGetStop(t *testing.T) {
	m := NewManager()
	defer m.Close()

	ad := &fakeAdapter{lines: []device.LogLine{
		mkLine(0, "hello"),
		mkLine(1, "world"),
		mkLine(2, "foo"),
	}}
	s, err := m.Start(context.Background(), ad, StartParams{
		Device:   "iPad",
		DeviceID: "udid-1",
		Filter:   device.LogFilter{Process: "TestApp"},
		Owner:    "tester",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.ID == "" {
		t.Fatal("Start returned empty ID")
	}

	// Wait for the adapter to push all lines.
	waitFor(t, 2*time.Second, func() bool {
		got, _ := s.snapshot()
		return len(got) >= 3
	})

	res, err := m.Stop(s.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(res.Lines) != 3 {
		t.Errorf("Stop: got %d lines; want 3", len(res.Lines))
	}
	if res.DroppedLines != 0 {
		t.Errorf("Stop: dropped=%d; want 0", res.DroppedLines)
	}

	if _, err := m.Stop(s.ID); err == nil {
		t.Error("Stop on already-stopped session: want error, got nil")
	}
	if _, err := m.Get(s.ID); err == nil {
		t.Error("Get on stopped session: want error, got nil")
	}

	ad.mu.Lock()
	if ad.lastFilt.Process != "TestApp" {
		t.Errorf("adapter saw filter Process=%q; want TestApp", ad.lastFilt.Process)
	}
	ad.mu.Unlock()
}

func TestManager_GetIsIncremental(t *testing.T) {
	m := NewManager()
	defer m.Close()
	ad := &fakeAdapter{lines: []device.LogLine{
		mkLine(0, "a"),
		mkLine(1, "b"),
		mkLine(2, "c"),
		mkLine(3, "d"),
	}, lineDelay: 20 * time.Millisecond}
	s, err := m.Start(context.Background(), ad, StartParams{DeviceID: "x"})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for at least 2 lines.
	waitFor(t, 2*time.Second, func() bool {
		got, _ := s.snapshot()
		return len(got) >= 2
	})
	first, err := m.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(first.Lines) < 1 {
		t.Errorf("first Get: got %d lines; want >=1", len(first.Lines))
	}

	// Wait for more lines to land after the drain.
	waitFor(t, 2*time.Second, func() bool {
		got, _ := s.snapshot()
		return len(got) >= 1
	})
	second, err := m.Get(s.ID)
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if len(second.Lines) < 1 {
		t.Errorf("second Get: got %d lines; want >=1 (capture should resume after drain)", len(second.Lines))
	}

	// Total across both Gets should equal what was pushed (4).
	total := len(first.Lines) + len(second.Lines)
	for total < 4 {
		waitFor(t, 1*time.Second, func() bool {
			got, _ := s.snapshot()
			return len(got) > 0
		})
		more, err := m.Get(s.ID)
		if err != nil {
			break
		}
		total += len(more.Lines)
		if len(more.Lines) == 0 {
			break
		}
	}
	if total != 4 {
		t.Errorf("total drained across Gets = %d; want 4", total)
	}
}

func TestManager_FIFOEvictionOnMaxLines(t *testing.T) {
	m := NewManager()
	defer m.Close()
	lines := make([]device.LogLine, 20)
	for i := range lines {
		lines[i] = mkLine(i, "x")
	}
	ad := &fakeAdapter{lines: lines}
	s, err := m.Start(context.Background(), ad, StartParams{
		DeviceID: "x",
		MaxLines: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		got, dropped := s.snapshot()
		return len(got) == 5 && dropped == 15
	})
	res, _ := m.Stop(s.ID)
	if len(res.Lines) != 5 {
		t.Errorf("Stop after MaxLines=5 cap: got %d; want 5", len(res.Lines))
	}
	if res.DroppedLines != 15 {
		t.Errorf("dropped_lines: got %d; want 15", res.DroppedLines)
	}
	// The 5 retained should be the most recent ones (indices 15..19).
	for i, ll := range res.Lines {
		want := fmt.Sprintf("[%d] x", 15+i)
		if ll.Message != want {
			t.Errorf("FIFO: line %d = %q; want %q", i, ll.Message, want)
		}
	}
}

func TestManager_TTLExpiry(t *testing.T) {
	m := NewManager()
	defer m.Close()
	ad := &fakeAdapter{lines: []device.LogLine{mkLine(0, "hi")}}
	s, err := m.Start(context.Background(), ad, StartParams{
		DeviceID: "x",
		TTL:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait past TTL — sweeper runs every 30s in production. For the
	// test we manually invoke expiry by polling via Stop, which is what
	// the sweeper does internally. Instead: assert the session WOULD
	// expire by checking the idle() helper directly.
	time.Sleep(100 * time.Millisecond)
	if !s.idle(time.Now()) {
		t.Fatalf("session not idle past TTL+slop")
	}
}

func TestManager_StartRequiresDeviceID(t *testing.T) {
	m := NewManager()
	defer m.Close()
	_, err := m.Start(context.Background(), &fakeAdapter{}, StartParams{Device: "iPad"})
	if err == nil {
		t.Fatal("Start with empty DeviceID: want error")
	}
}

func TestManager_StartRejectsTTLOverMax(t *testing.T) {
	m := NewManager()
	defer m.Close()
	_, err := m.Start(context.Background(), &fakeAdapter{}, StartParams{
		DeviceID: "x",
		TTL:      MaxTTL + time.Hour,
	})
	if err == nil {
		t.Fatal("Start with TTL > MaxTTL: want error")
	}
}

func TestManager_List(t *testing.T) {
	m := NewManager()
	defer m.Close()
	ad := &fakeAdapter{lines: []device.LogLine{mkLine(0, "a")}}
	s1, _ := m.Start(context.Background(), ad, StartParams{Device: "iPad", DeviceID: "x", Owner: "alice"})
	s2, _ := m.Start(context.Background(), ad, StartParams{Device: "iPhone", DeviceID: "y", Owner: "bob"})
	infos := m.List()
	if len(infos) != 2 {
		t.Fatalf("List: got %d; want 2", len(infos))
	}
	seen := map[string]bool{}
	for _, info := range infos {
		seen[info.SessionID] = true
	}
	if !seen[s1.ID] || !seen[s2.ID] {
		t.Errorf("List missing one of the IDs: got %v", seen)
	}
}

func TestManager_Close(t *testing.T) {
	m := NewManager()
	ad := &fakeAdapter{lines: []device.LogLine{mkLine(0, "a")}}
	s, _ := m.Start(context.Background(), ad, StartParams{DeviceID: "x"})
	m.Close()
	if _, err := m.Get(s.ID); err == nil {
		t.Error("Get after Close: want error, got nil")
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
