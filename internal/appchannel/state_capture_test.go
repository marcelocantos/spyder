// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStateApp is a minimal app that handles `hello` (advertising
// state_query support + an optional slices list) and responds to
// state_query{slice} with a counter-incrementing payload so the
// capture poller sees distinguishable samples.
type fakeStateApp struct {
	t       *testing.T
	conn    net.Conn
	counter atomic.Int64
}

func newFakeStateApp(t *testing.T, port int, advertisedSlices []string) *fakeStateApp {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	helloParams, _ := PackParams(Hello{
		AppName:    "fake-state",
		AppVersion: "test",
		Methods:    []string{MethodPing, MethodStateQuery},
		Slices:     advertisedSlices,
	})
	if err := WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if _, err := ReadFrame(conn); err != nil {
		t.Fatalf("hello ack: %v", err)
	}
	app := &fakeStateApp{t: t, conn: conn}
	go app.serve()
	return app
}

func (a *fakeStateApp) serve() {
	for {
		env, err := ReadFrame(a.conn)
		if err != nil {
			return
		}
		if !env.IsRequest() {
			continue
		}
		if env.Method == MethodStateQuery {
			n := a.counter.Add(1)
			var p struct {
				Slice string `msgpack:"slice"`
			}
			_ = UnpackParams(env.Params, &p)
			raw, _ := PackParams(map[string]any{
				"slice":  p.Slice,
				"tick":   n,
				"sample": fmt.Sprintf("tick %d", n),
			})
			_ = WriteFrame(a.conn, &Envelope{ID: env.ID, Result: raw})
			continue
		}
		// Ping or anything else: ack with empty result.
		raw, _ := PackParams(map[string]bool{"ok": true})
		_ = WriteFrame(a.conn, &Envelope{ID: env.ID, Result: raw})
	}
}

func (a *fakeStateApp) close() { _ = a.conn.Close() }

func startStateAppAndSession(t *testing.T, slices []string) (*Manager, *Listener, *fakeStateApp, *Session) {
	t.Helper()
	m := NewManager()
	t.Cleanup(m.Close)
	l, err := m.Start(StartParams{Owner: "state-capture-test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)
	app := newFakeStateApp(t, l.Port, slices)
	t.Cleanup(app.close)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ss := l.Sessions()
		if len(ss) > 0 && ss[0].HelloInfo() != nil {
			return m, l, app, ss[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session accepted within 2s")
	return nil, nil, nil, nil
}

func TestHelloAdvertisesSlices(t *testing.T) {
	_, _, _, s := startStateAppAndSession(t, []string{"scene", "physics", "hud"})
	hello := s.HelloInfo()
	if hello == nil {
		t.Fatal("HelloInfo nil")
	}
	if len(hello.Slices) != 3 {
		t.Fatalf("Slices = %v; want 3 entries", hello.Slices)
	}
	for i, want := range []string{"scene", "physics", "hud"} {
		if hello.Slices[i] != want {
			t.Errorf("Slices[%d] = %q; want %q", i, hello.Slices[i], want)
		}
	}
}

func TestStateCapture_RoundTrip(t *testing.T) {
	_, _, _, s := startStateAppAndSession(t, []string{"scene"})

	cap, err := s.StartStateCapture("scene", 30*time.Millisecond)
	if err != nil {
		t.Fatalf("StartStateCapture: %v", err)
	}
	if cap.ID == "" {
		t.Fatal("capture ID empty")
	}

	// Wait long enough to accumulate at least 3 samples.
	time.Sleep(150 * time.Millisecond)

	r, err := s.GetStateCapture(cap.ID)
	if err != nil {
		t.Fatalf("GetStateCapture: %v", err)
	}
	if len(r.Samples) < 3 {
		t.Errorf("Samples = %d; want ≥3", len(r.Samples))
	}
	for i, sample := range r.Samples {
		if sample.Timestamp.IsZero() {
			t.Errorf("Samples[%d].Timestamp zero", i)
		}
		if len(sample.Data) == 0 {
			t.Errorf("Samples[%d].Data empty", i)
		}
	}

	// Get drains: second Get sees only new arrivals.
	time.Sleep(100 * time.Millisecond)
	r2, err := s.GetStateCapture(cap.ID)
	if err != nil {
		t.Fatalf("second GetStateCapture: %v", err)
	}
	if len(r2.Samples) == 0 {
		t.Error("second Get returned 0 samples; capture should still be running")
	}

	// Stop drains and tears down.
	stop, err := s.StopStateCapture(cap.ID)
	if err != nil {
		t.Fatalf("StopStateCapture: %v", err)
	}
	_ = stop
	if _, err := s.StopStateCapture(cap.ID); err == nil {
		t.Error("second Stop should error")
	}
}

func TestStateCapture_UnsupportedMethod(t *testing.T) {
	// App advertises only `ping` — not state_query.
	m := NewManager()
	t.Cleanup(m.Close)
	l, _ := m.Start(StartParams{Owner: "test"})
	t.Cleanup(l.Stop)

	conn, _ := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port))
	defer conn.Close()
	helloParams, _ := PackParams(Hello{
		AppName: "no-state", AppVersion: "test",
		Methods: []string{MethodPing}, // no state_query
	})
	_ = WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: helloParams})
	_, _ = ReadFrame(conn)

	deadline := time.Now().Add(2 * time.Second)
	var s *Session
	for time.Now().Before(deadline) {
		ss := l.Sessions()
		if len(ss) > 0 && ss[0].HelloInfo() != nil {
			s = ss[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s == nil {
		t.Fatal("no session")
	}

	_, err := s.StartStateCapture("scene", 50*time.Millisecond)
	if err == nil {
		t.Fatal("StartStateCapture should error when app doesn't support state_query")
	}
}

func TestStateCapture_EmptySliceRejected(t *testing.T) {
	_, _, _, s := startStateAppAndSession(t, []string{"scene"})
	if _, err := s.StartStateCapture("", 50*time.Millisecond); err == nil {
		t.Fatal("empty slice should error")
	}
}

func TestStateCapture_DefaultInterval(t *testing.T) {
	_, _, _, s := startStateAppAndSession(t, []string{"scene"})
	cap, err := s.StartStateCapture("scene", 0)
	if err != nil {
		t.Fatalf("StartStateCapture: %v", err)
	}
	defer func() { _, _ = s.StopStateCapture(cap.ID) }()
	if cap.Interval != DefaultStateCaptureInterval {
		t.Errorf("Interval = %s; want default %s", cap.Interval, DefaultStateCaptureInterval)
	}
}

func TestStateCapture_BelowMinClamps(t *testing.T) {
	_, _, _, s := startStateAppAndSession(t, []string{"scene"})
	cap, err := s.StartStateCapture("scene", time.Millisecond) // 1ms < MinStateCaptureInterval
	if err != nil {
		t.Fatalf("StartStateCapture: %v", err)
	}
	defer func() { _, _ = s.StopStateCapture(cap.ID) }()
	if cap.Interval != MinStateCaptureInterval {
		t.Errorf("Interval = %s; want clamp to %s", cap.Interval, MinStateCaptureInterval)
	}
}

func TestStateCapture_ListReflectsActive(t *testing.T) {
	_, _, _, s := startStateAppAndSession(t, []string{"scene", "physics"})
	c1, _ := s.StartStateCapture("scene", 50*time.Millisecond)
	c2, _ := s.StartStateCapture("physics", 50*time.Millisecond)

	infos := s.ListStateCaptures()
	if len(infos) != 2 {
		t.Errorf("List len = %d; want 2", len(infos))
	}

	_, _ = s.StopStateCapture(c1.ID)
	infos = s.ListStateCaptures()
	if len(infos) != 1 {
		t.Errorf("List len after stop one = %d; want 1", len(infos))
	}
	if infos[0].CaptureID != c2.ID {
		t.Errorf("remaining capture = %q; want %q", infos[0].CaptureID, c2.ID)
	}
	_, _ = s.StopStateCapture(c2.ID)
}

func TestStateCapture_SessionCloseStopsPoller(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)
	l, _ := m.Start(StartParams{Owner: "test"})
	defer l.Stop()
	app := newFakeStateApp(t, l.Port, []string{"scene"})

	deadline := time.Now().Add(2 * time.Second)
	var s *Session
	for time.Now().Before(deadline) {
		ss := l.Sessions()
		if len(ss) > 0 && ss[0].HelloInfo() != nil {
			s = ss[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s == nil {
		t.Fatal("no session")
	}

	cap, _ := s.StartStateCapture("scene", 30*time.Millisecond)

	// Close the session — the capture goroutine must exit promptly.
	app.close()
	_ = s.Close()

	// Verify done channel fires within reasonable time.
	select {
	case <-cap.done:
	case <-time.After(time.Second):
		t.Fatal("state capture goroutine did not exit after session close")
	}
}
