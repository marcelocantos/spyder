// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// fakeApp connects to a Listener, performs Hello, and runs a small
// dispatch loop. It implements a few methods used by the tests.
type fakeApp struct {
	t       *testing.T
	addr    string
	conn    net.Conn
	methods []string
	// handlers map method name → handler. Each returns the result
	// payload (will be MessagePack-encoded by PackParams) or an
	// *RPCError.
	handlers map[string]func(params []byte) (any, error)
}

func newFakeApp(t *testing.T, addr string, methods []string) *fakeApp {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	app := &fakeApp{t: t, addr: addr, conn: conn, methods: methods, handlers: map[string]func([]byte) (any, error){}}
	// Send hello.
	helloParams, _ := PackParams(Hello{
		AppName:    "fake",
		AppVersion: "test",
		Methods:    methods,
	})
	if err := WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	ack, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("read hello ack: %v", err)
	}
	if ack.ID != 1 || ack.Error != nil {
		t.Fatalf("bad hello ack: %+v", ack)
	}
	go app.dispatchLoop()
	return app
}

func (a *fakeApp) on(method string, h func(params []byte) (any, error)) {
	a.handlers[method] = h
}

func (a *fakeApp) dispatchLoop() {
	for {
		env, err := ReadFrame(a.conn)
		if err != nil {
			return
		}
		if env.IsRequest() {
			h, ok := a.handlers[env.Method]
			if !ok {
				_ = WriteFrame(a.conn, &Envelope{
					ID:    env.ID,
					Error: &RPCError{Code: ErrCodeMethodNotFound, Message: "no handler for " + env.Method},
				})
				continue
			}
			res, err := h(env.Params)
			if err != nil {
				if rerr, ok := err.(*RPCError); ok {
					_ = WriteFrame(a.conn, &Envelope{ID: env.ID, Error: rerr})
				} else {
					_ = WriteFrame(a.conn, &Envelope{ID: env.ID, Error: &RPCError{Code: ErrCodeInternal, Message: err.Error()}})
				}
				continue
			}
			raw, _ := PackParams(res)
			_ = WriteFrame(a.conn, &Envelope{ID: env.ID, Result: raw})
		}
	}
}

func (a *fakeApp) push(method string, payload any) error {
	raw, _ := PackParams(payload)
	return WriteFrame(a.conn, &Envelope{Method: method, Params: raw})
}

func (a *fakeApp) close() {
	_ = a.conn.Close()
}

// helpers --------------------------------------------------------------

func startManagerAndListener(t *testing.T) (*Manager, *Listener) {
	t.Helper()
	m := NewManager()
	t.Cleanup(m.Close)
	l, err := m.Start(StartParams{Owner: "test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)
	return m, l
}

func waitForSession(t *testing.T, l *Listener) *Session {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sessions := l.Sessions()
		if len(sessions) > 0 {
			return sessions[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session accepted within 2s")
	return nil
}

// tests ----------------------------------------------------------------

func TestHandshakeBasic(t *testing.T) {
	_, l := startManagerAndListener(t)
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), []string{MethodPing, MethodQuit})
	defer app.close()

	s := waitForSession(t, l)
	h := s.HelloInfo()
	if h == nil {
		t.Fatal("HelloInfo nil after handshake")
	}
	if h.AppName != "fake" {
		t.Errorf("AppName = %q; want fake", h.AppName)
	}
	if !s.Supports(MethodPing) {
		t.Error("Supports(ping) false")
	}
	if !s.Supports(MethodQuit) {
		t.Error("Supports(quit) false")
	}
	if s.Supports(MethodPause) {
		t.Error("Supports(pause) true; app didn't advertise it")
	}
}

func TestCallRoundTrip(t *testing.T) {
	_, l := startManagerAndListener(t)
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), []string{MethodPing})
	defer app.close()

	app.on(MethodPing, func(_ []byte) (any, error) {
		return map[string]int64{"ts": 42}, nil
	})

	s := waitForSession(t, l)
	res, err := s.Call(context.Background(), MethodPing, nil, time.Second)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got map[string]int64
	if err := UnpackParams(res, &got); err != nil {
		t.Fatalf("UnpackParams: %v", err)
	}
	if got["ts"] != 42 {
		t.Errorf("ts = %d; want 42", got["ts"])
	}
}

func TestCallErrorPropagates(t *testing.T) {
	_, l := startManagerAndListener(t)
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), []string{"oops"})
	defer app.close()

	app.on("oops", func(_ []byte) (any, error) {
		return nil, &RPCError{Code: -32099, Message: "synthetic failure"}
	})

	s := waitForSession(t, l)
	_, err := s.Call(context.Background(), "oops", nil, time.Second)
	rerr, ok := err.(*RPCError)
	if !ok {
		t.Fatalf("err type = %T; want *RPCError", err)
	}
	if rerr.Code != -32099 {
		t.Errorf("Code = %d; want -32099", rerr.Code)
	}
}

func TestCallUnsupportedMethod(t *testing.T) {
	_, l := startManagerAndListener(t)
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), []string{MethodPing})
	defer app.close()
	s := waitForSession(t, l)

	_, err := s.Call(context.Background(), MethodQuit, nil, time.Second)
	rerr, ok := err.(*RPCError)
	if !ok || rerr.Code != ErrCodeUnsupported {
		t.Fatalf("err = %v; want ErrCodeUnsupported", err)
	}
}

func TestCallTimeout(t *testing.T) {
	_, l := startManagerAndListener(t)
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), []string{"slow"})
	defer app.close()
	app.on("slow", func(_ []byte) (any, error) {
		time.Sleep(500 * time.Millisecond)
		return "ok", nil
	})
	s := waitForSession(t, l)

	_, err := s.Call(context.Background(), "slow", nil, 100*time.Millisecond)
	rerr, ok := err.(*RPCError)
	if !ok || rerr.Code != ErrCodeTimeout {
		t.Fatalf("err = %v; want ErrCodeTimeout", err)
	}
}

func TestLogPushBuffered(t *testing.T) {
	_, l := startManagerAndListener(t)
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), []string{})
	defer app.close()
	s := waitForSession(t, l)

	for i := 0; i < 3; i++ {
		_ = app.push(PushLog, LogPush{
			Timestamp: int64(1000 + i),
			Level:     "info",
			Format:    fmt.Sprintf("line %d", i),
		})
	}
	// Wait for them to land.
	deadline := time.Now().Add(time.Second)
	var logs []LogPush
	for time.Now().Before(deadline) {
		logs, _ = s.DrainLogs()
		if len(logs) == 3 {
			break
		}
		// Re-buffer if we drained early.
		for _, lg := range logs {
			s.mu.Lock()
			s.logBuf = append(s.logBuf, lg)
			s.mu.Unlock()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(logs) != 3 {
		t.Fatalf("logs = %d; want 3", len(logs))
	}
	for i, lg := range logs {
		if lg.Format != fmt.Sprintf("line %d", i) {
			t.Errorf("logs[%d].Format = %q", i, lg.Format)
		}
		if lg.Source == "" {
			t.Errorf("logs[%d].Source empty", i)
		}
	}
}

func TestPerfPushBuffered(t *testing.T) {
	_, l := startManagerAndListener(t)
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), []string{})
	defer app.close()
	s := waitForSession(t, l)

	_ = app.push(PushPerfCounters, PerfPush{
		Timestamp: 100,
		Samples:   map[string]float64{"frame_ms": 16.6, "allocs": 42},
	})
	deadline := time.Now().Add(time.Second)
	var perf []PerfPush
	for time.Now().Before(deadline) {
		perf, _ = s.DrainPerf()
		if len(perf) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(perf) != 1 {
		t.Fatalf("perf = %d; want 1", len(perf))
	}
	if perf[0].Samples["frame_ms"] != 16.6 {
		t.Errorf("frame_ms = %v; want 16.6", perf[0].Samples["frame_ms"])
	}
}

func TestSessionClose_WakesPendingCalls(t *testing.T) {
	_, l := startManagerAndListener(t)
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), []string{"slow"})
	app.on("slow", func(_ []byte) (any, error) {
		time.Sleep(2 * time.Second)
		return nil, nil
	})
	s := waitForSession(t, l)

	errCh := make(chan error, 1)
	go func() {
		_, err := s.Call(context.Background(), "slow", nil, 5*time.Second)
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	app.close() // simulate app disconnect

	select {
	case err := <-errCh:
		rerr, ok := err.(*RPCError)
		if !ok || rerr.Code != ErrCodeNotConnected {
			t.Errorf("err = %v; want ErrCodeNotConnected", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not unblock after session close")
	}
}

func TestRejectsNonHelloFirstMessage(t *testing.T) {
	_, l := startManagerAndListener(t)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send a `ping` instead of hello.
	_ = WriteFrame(conn, &Envelope{ID: 1, Method: MethodPing})
	// Expect an error envelope, then the connection to close.
	ack, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if ack.Error == nil || ack.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("ack = %+v; want InvalidRequest error", ack)
	}
}
