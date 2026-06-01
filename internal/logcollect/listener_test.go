// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package logcollect

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestStartGetStop_RoundTrip(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)

	sess, err := m.Start(StartParams{Owner: "test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.Port == 0 {
		t.Fatal("Start returned port 0")
	}

	// Dial and send three lines.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sess.Port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	for i := 0; i < 3; i++ {
		fmt.Fprintf(conn, "line %d\n", i)
	}
	_ = conn.Close()

	// Wait for all three lines to land in the buffer *before* draining
	// — polling Get itself would drain the buffer between checks and
	// cause spurious failures on slower runners.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info := m.List()
		if len(info) > 0 && info[0].BufferLines == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, err := m.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(r.Lines) != 3 {
		t.Fatalf("Lines = %d; want 3 (got: %#v)", len(r.Lines), r.Lines)
	}
	for i, l := range r.Lines {
		want := fmt.Sprintf("line %d", i)
		if l.Message != want {
			t.Errorf("Lines[%d] = %q; want %q", i, l.Message, want)
		}
		if l.Source == "" {
			t.Errorf("Lines[%d] missing source", i)
		}
	}

	// Second Get returns nothing — drain emptied the buffer.
	r2, err := m.Get(sess.ID)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if len(r2.Lines) != 0 {
		t.Errorf("second Get returned %d lines; want 0", len(r2.Lines))
	}

	stop, err := m.Stop(sess.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(stop.Lines) != 0 {
		t.Errorf("Stop after drain returned %d lines; want 0", len(stop.Lines))
	}

	// Second Stop errors.
	if _, err := m.Stop(sess.ID); err == nil {
		t.Error("second Stop should error")
	}
}

func TestStop_DrainsRemaining(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)

	sess, _ := m.Start(StartParams{})
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sess.Port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	fmt.Fprintln(conn, "kept-for-stop")
	_ = conn.Close()

	// Wait for line to arrive.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		info := m.List()
		if len(info) > 0 && info[0].BufferLines == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	stop, err := m.Stop(sess.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(stop.Lines) != 1 || stop.Lines[0].Message != "kept-for-stop" {
		t.Errorf("Stop drain = %#v; want one line 'kept-for-stop'", stop.Lines)
	}
}

func TestPortIsolation_TwoSessionsTwoPorts(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)

	a, _ := m.Start(StartParams{Owner: "a"})
	b, _ := m.Start(StartParams{Owner: "b"})
	if a.Port == b.Port {
		t.Fatalf("two sessions returned the same port %d", a.Port)
	}

	// Send to A only — B's buffer stays empty.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", a.Port))
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	fmt.Fprintln(conn, "for-A")
	_ = conn.Close()

	deadline := time.Now().Add(time.Second)
	var got *GetResult
	for time.Now().Before(deadline) {
		got, _ = m.Get(a.ID)
		if len(got.Lines) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got.Lines) != 1 {
		t.Errorf("A got %d lines; want 1", len(got.Lines))
	}
	gotB, _ := m.Get(b.ID)
	if len(gotB.Lines) != 0 {
		t.Errorf("B got %d lines; want 0 (port isolation broken)", len(gotB.Lines))
	}
}

func TestReconnectMergesIntoSession(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)

	sess, _ := m.Start(StartParams{})

	for i := 0; i < 2; i++ {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sess.Port))
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		fmt.Fprintf(conn, "conn%d-line\n", i)
		_ = conn.Close()
	}

	// Wait for both lines to land in the buffer without draining mid-poll.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		info := m.List()
		if len(info) > 0 && info[0].BufferLines == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, _ := m.Get(sess.ID)
	if len(r.Lines) != 2 {
		t.Fatalf("Lines = %d; want 2 lines from two reconnects", len(r.Lines))
	}
	msgs := []string{r.Lines[0].Message, r.Lines[1].Message}
	want := []string{"conn0-line", "conn1-line"}
	// Order should be FIFO by arrival time.
	for i := range want {
		if !strings.Contains(msgs[i], want[i]) {
			t.Errorf("Lines[%d] = %q; want substring %q", i, msgs[i], want[i])
		}
	}
}

func TestList_ExposesPortAndCounters(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)

	sess, _ := m.Start(StartParams{Owner: "list-test"})

	info := m.List()
	if len(info) != 1 {
		t.Fatalf("List len = %d; want 1", len(info))
	}
	if info[0].Port != sess.Port {
		t.Errorf("List Port = %d; want %d", info[0].Port, sess.Port)
	}
	if info[0].Owner != "list-test" {
		t.Errorf("List Owner = %q; want list-test", info[0].Owner)
	}
}

func TestLANHosts_ReturnsAtLeastOne(t *testing.T) {
	// On any machine that's on a network, LANHosts should return ≥1
	// non-loopback IPv4. Skip if the test runner is offline (rare).
	hs, err := LANHosts()
	if err != nil {
		t.Fatalf("LANHosts: %v", err)
	}
	if len(hs) == 0 {
		t.Skip("no LAN IPv4 addresses on this host; skipping")
	}
	for _, h := range hs {
		if h == "127.0.0.1" || strings.HasPrefix(h, "169.254.") {
			t.Errorf("LANHosts returned loopback/link-local: %q", h)
		}
	}
}
