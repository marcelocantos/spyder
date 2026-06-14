// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

// smokeClient is a minimal in-process app that connects to a spyder
// appchannel listener, completes the hello handshake, and implements
// the full v1 method set. Used by the integration tests to exercise
// every app_* MCP tool end-to-end.
type smokeClient struct {
	t    *testing.T
	conn net.Conn
	stop chan struct{}
	// state observable from outside for assertions
	quitCalled       bool
	flushCalled      bool
	backgroundCalled bool
	foregroundCalled bool
	lowMemCalled     bool
	pauseCalled      bool
	resumeCalled     bool
	stepFrames       int
	speedMult        float64
	lastInput        map[string]any
	restoredState    []byte
}

func dialSmoke(t *testing.T, port int, methods []string) *smokeClient {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	helloParams, _ := appchannel.PackParams(appchannel.Hello{
		AppName:    "smoke",
		AppVersion: "1.0",
		Methods:    methods,
	})
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	ack, err := appchannel.ReadFrame(conn)
	if err != nil {
		t.Fatalf("hello ack: %v", err)
	}
	if ack.Error != nil {
		t.Fatalf("hello errored: %v", ack.Error)
	}
	c := &smokeClient{t: t, conn: conn, stop: make(chan struct{})}
	go c.serve()
	return c
}

func (c *smokeClient) serve() {
	for {
		env, err := appchannel.ReadFrame(c.conn)
		if err != nil {
			close(c.stop)
			return
		}
		if !env.IsRequest() {
			continue
		}
		var result any
		var rerr *appchannel.RPCError
		switch env.Method {
		case appchannel.MethodPing:
			result = map[string]int64{"ts": time.Now().UnixNano()}
		case appchannel.MethodQuit:
			c.quitCalled = true
			result = map[string]bool{"shutting_down": true}
		case appchannel.MethodFlush:
			c.flushCalled = true
			result = map[string][]string{"flushed": {"log", "render"}}
		case appchannel.MethodBackgrounded:
			c.backgroundCalled = true
			result = map[string]bool{"ok": true}
		case appchannel.MethodForegrounded:
			c.foregroundCalled = true
			result = map[string]bool{"ok": true}
		case appchannel.MethodLowMemoryWarning:
			c.lowMemCalled = true
			result = map[string]bool{"posted": true}
		case appchannel.MethodPause:
			c.pauseCalled = true
			result = map[string]bool{"paused": true}
		case appchannel.MethodResume:
			c.resumeCalled = true
			result = map[string]bool{"resumed": true}
		case appchannel.MethodStep:
			var p struct {
				Frames int `msgpack:"frames"`
			}
			_ = appchannel.UnpackParams(env.Params, &p)
			c.stepFrames = p.Frames
			result = map[string]int{"stepped": p.Frames}
		case appchannel.MethodSpeed:
			var p struct {
				Multiplier float64 `msgpack:"multiplier"`
			}
			_ = appchannel.UnpackParams(env.Params, &p)
			c.speedMult = p.Multiplier
			result = map[string]float64{"speed": p.Multiplier}
		case appchannel.MethodInputInject:
			var p map[string]any
			_ = appchannel.UnpackParams(env.Params, &p)
			c.lastInput = p
			result = map[string]bool{"injected": true}
		case appchannel.MethodStateQuery:
			var p struct {
				Slice string `msgpack:"slice"`
			}
			_ = appchannel.UnpackParams(env.Params, &p)
			result = map[string]any{
				"slice":  p.Slice,
				"sample": "data for " + p.Slice,
				"nested": map[string]int{"a": 1, "b": 2},
			}
		case appchannel.MethodSaveState:
			result = map[string][]byte{"state": []byte("opaque-state-blob-" + time.Now().Format("150405"))}
		case appchannel.MethodRestoreState:
			var p struct {
				State []byte `msgpack:"state"`
			}
			_ = appchannel.UnpackParams(env.Params, &p)
			c.restoredState = p.State
			result = map[string]int{"restored_bytes": len(p.State)}
		case appchannel.MethodScreenshotApp:
			result = map[string]any{
				"format": "png",
				"width":  100,
				"height": 50,
				"data":   []byte("\x89PNG\r\n\x1a\n fake png bytes"),
			}
		default:
			rerr = &appchannel.RPCError{Code: appchannel.ErrCodeMethodNotFound, Message: "no handler"}
		}
		if rerr != nil {
			_ = appchannel.WriteFrame(c.conn, &appchannel.Envelope{ID: env.ID, Error: rerr})
		} else {
			raw, _ := appchannel.PackParams(result)
			_ = appchannel.WriteFrame(c.conn, &appchannel.Envelope{ID: env.ID, Result: raw})
		}
	}
}

func (c *smokeClient) close() {
	_ = c.conn.Close()
}

func (c *smokeClient) pushLog(level, format string) {
	raw, _ := appchannel.PackParams(appchannel.LogPush{
		Timestamp: time.Now().UnixNano(),
		Level:     level,
		Format:    format,
	})
	_ = appchannel.WriteFrame(c.conn, &appchannel.Envelope{Method: appchannel.PushLog, Params: raw})
}

func (c *smokeClient) pushPerf(samples map[string]float64) {
	raw, _ := appchannel.PackParams(appchannel.PerfPush{
		Timestamp: time.Now().UnixNano(),
		Samples:   samples,
	})
	_ = appchannel.WriteFrame(c.conn, &appchannel.Envelope{Method: appchannel.PushPerfCounters, Params: raw})
}

// helpers --------------------------------------------------------------

func startAppChannelHandler(t *testing.T) *Handler {
	t.Helper()
	h := newTestHandler(t)
	h.appChannel = appchannel.NewManager()
	h.appChannelListeners = map[string]*appchannel.Listener{}
	t.Cleanup(h.appChannel.Close)
	return h
}

func openListener(t *testing.T, h *Handler) (listenerID string, port int) {
	t.Helper()
	r := dispatchJSON(t, h, "app_channel_start", map[string]any{"owner": "test"})
	if r.IsError {
		t.Fatalf("app_channel_start: %s", resultText(t, &r))
	}
	var resp struct {
		ListenerID string   `json:"listener_id"`
		Port       int      `json:"port"`
		Hosts      []string `json:"hosts"`
	}
	if err := json.Unmarshal([]byte(resultText(t, &r)), &resp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	return resp.ListenerID, resp.Port
}

func waitForAppSession(t *testing.T, h *Handler) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sessions := h.appChannel.Sessions()
		if len(sessions) > 0 && sessions[0].HelloInfo() != nil {
			return sessions[0].ID
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session accepted within 2s")
	return ""
}

// tests ----------------------------------------------------------------

func TestAppChannel_StartListAndStop(t *testing.T) {
	h := startAppChannelHandler(t)
	id, port := openListener(t, h)
	if port == 0 {
		t.Fatal("port = 0")
	}

	// list should show one (no sessions yet because no app connected).
	r := dispatchJSON(t, h, "app_channel_list", nil)
	if r.IsError {
		t.Fatalf("app_channel_list: %s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "[]") {
		// no app sessions yet
		t.Logf("list (pre-connect): %s", resultText(t, &r))
	}

	// stop the listener.
	r = dispatchJSON(t, h, "app_channel_stop", map[string]any{"listener_id": id})
	if r.IsError {
		t.Fatalf("app_channel_stop: %s", resultText(t, &r))
	}

	// stopping again errors.
	r = dispatchJSON(t, h, "app_channel_stop", map[string]any{"listener_id": id})
	if !r.IsError {
		t.Error("second stop should error")
	}
}

func TestAppChannel_FullMethodSweep(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)

	client := dialSmoke(t, port, []string{
		appchannel.MethodPing, appchannel.MethodQuit, appchannel.MethodFlush,
		appchannel.MethodBackgrounded, appchannel.MethodForegrounded,
		appchannel.MethodLowMemoryWarning,
		appchannel.MethodPause, appchannel.MethodResume,
		appchannel.MethodStep, appchannel.MethodSpeed,
		appchannel.MethodInputInject, appchannel.MethodStateQuery,
		appchannel.MethodSaveState, appchannel.MethodRestoreState,
		appchannel.MethodScreenshotApp,
	})
	defer client.close()
	sessionID := waitForAppSession(t, h)

	type call struct {
		name   string
		args   map[string]any
		assert func(t *testing.T, body string)
	}
	calls := []call{
		{"app_ping", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			if !strings.Contains(body, "ts") {
				t.Errorf("ping body missing ts: %s", body)
			}
		}},
		{"app_quit", map[string]any{"session_id": sessionID, "timeout_ms": 1000.0}, func(t *testing.T, body string) {
			if !strings.Contains(body, "quit acknowledged") {
				t.Errorf("quit body: %s", body)
			}
			if !client.quitCalled {
				t.Error("quit handler not called")
			}
		}},
		{"app_flush", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			if !client.flushCalled {
				t.Error("flush handler not called")
			}
		}},
		{"app_background", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			if !client.backgroundCalled {
				t.Error("background handler not called")
			}
		}},
		{"app_foreground", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			if !client.foregroundCalled {
				t.Error("foreground handler not called")
			}
		}},
		{"app_low_memory", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			if !client.lowMemCalled {
				t.Error("low_memory handler not called")
			}
		}},
		{"app_pause", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			if !client.pauseCalled {
				t.Error("pause handler not called")
			}
		}},
		{"app_resume", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			if !client.resumeCalled {
				t.Error("resume handler not called")
			}
		}},
		{"app_step", map[string]any{"session_id": sessionID, "frames": 5.0}, func(t *testing.T, body string) {
			if client.stepFrames != 5 {
				t.Errorf("stepFrames = %d; want 5", client.stepFrames)
			}
		}},
		{"app_speed", map[string]any{"session_id": sessionID, "multiplier": 0.5}, func(t *testing.T, body string) {
			if client.speedMult != 0.5 {
				t.Errorf("speedMult = %v; want 0.5", client.speedMult)
			}
		}},
		{"app_input", map[string]any{"session_id": sessionID, "type": "finger_down", "x": 0.5, "y": 0.5}, func(t *testing.T, body string) {
			if client.lastInput["type"] != "finger_down" {
				t.Errorf("input = %v", client.lastInput)
			}
		}},
		{"app_state", map[string]any{"session_id": sessionID, "slice": "scene"}, func(t *testing.T, body string) {
			if !strings.Contains(body, "data for scene") {
				t.Errorf("state body: %s", body)
			}
		}},
		{"app_save_state", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			if !strings.Contains(body, "state_b64") {
				t.Errorf("save_state body: %s", body)
			}
			// Validate the b64 is decodable.
			var resp struct {
				StateB64 string `json:"state_b64"`
				Size     int    `json:"size"`
			}
			_ = json.Unmarshal([]byte(body), &resp)
			if resp.Size == 0 {
				t.Error("size = 0; want > 0")
			}
			if _, err := base64.StdEncoding.DecodeString(resp.StateB64); err != nil {
				t.Errorf("state_b64 not decodable: %v", err)
			}
		}},
		{"app_restore_state", map[string]any{"session_id": sessionID, "state_b64": base64.StdEncoding.EncodeToString([]byte("my-restored-state"))}, func(t *testing.T, body string) {
			if string(client.restoredState) != "my-restored-state" {
				t.Errorf("restored = %q", string(client.restoredState))
			}
		}},
		{"app_screenshot", map[string]any{"session_id": sessionID}, func(t *testing.T, body string) {
			// Screenshot returns an image content block; body inspection is
			// limited to verifying the dispatcher succeeded.
		}},
	}

	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			r := dispatchJSON(t, h, c.name, c.args)
			body := resultText(t, &r)
			if r.IsError {
				t.Fatalf("%s errored: %s", c.name, body)
			}
			if c.assert != nil {
				c.assert(t, body)
			}
		})
	}

	// Push-message paths: app pushes log + perf, agent drains.
	for i := 0; i < 3; i++ {
		client.pushLog("info", fmt.Sprintf("log line %d", i))
	}
	client.pushPerf(map[string]float64{"frame_ms": 16.6, "alloc": 1024})

	// Drain logs.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s, _ := h.appChannel.GetSession(sessionID)
		if s == nil {
			t.Fatal("session vanished")
		}
		s.HelloInfo() // touch
		logs, _ := s.DrainLogs()
		if len(logs) == 3 {
			break
		}
		// Re-stuff the buffer to keep polling.
		for _, lg := range logs {
			_ = lg // discard, we'll re-poll
		}
		time.Sleep(10 * time.Millisecond)
	}
	r := dispatchJSON(t, h, "app_perf_get", map[string]any{"session_id": sessionID})
	body := resultText(t, &r)
	if !strings.Contains(body, "frame_ms") {
		t.Errorf("perf_get body missing frame_ms: %s", body)
	}
}

func TestAppChannel_UnsupportedMethod(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmoke(t, port, []string{appchannel.MethodPing}) // only ping
	defer client.close()
	sessionID := waitForAppSession(t, h)

	// app_quit should fail with unsupported (app didn't advertise quit).
	r := dispatchJSON(t, h, "app_quit", map[string]any{"session_id": sessionID})
	if !r.IsError {
		t.Fatal("app_quit should error; app didn't advertise method")
	}
	if !strings.Contains(resultText(t, &r), "does not support") {
		t.Errorf("error should say 'does not support'; got %s", resultText(t, &r))
	}
}

func TestAppChannel_DefaultSessionWhenOnlyOne(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmoke(t, port, []string{appchannel.MethodPing})
	defer client.close()
	_ = waitForAppSession(t, h)

	// Don't pass session_id; should pick the only one.
	r := dispatchJSON(t, h, "app_ping", nil)
	if r.IsError {
		t.Fatalf("ping without session_id should succeed when one session: %s", resultText(t, &r))
	}
}

func TestAppChannel_SessionRequiredWhenMultiple(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	a := dialSmoke(t, port, []string{appchannel.MethodPing})
	defer a.close()
	_ = waitForAppSession(t, h)
	b := dialSmoke(t, port, []string{appchannel.MethodPing})
	defer b.close()
	// Wait for the second session to land too.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(h.appChannel.Sessions()) < 2 {
		time.Sleep(10 * time.Millisecond)
	}

	r := dispatchJSON(t, h, "app_ping", nil)
	if !r.IsError {
		t.Fatal("ping without session_id should error when 2 sessions")
	}
}

// Sanity: the daemon dispatcher recognises every new tool.
func TestAppChannel_DispatchSurfaceCoverage(t *testing.T) {
	h := startAppChannelHandler(t)
	expected := []string{
		"app_channel_start", "app_channel_stop", "app_channel_list",
		"app_ping", "app_quit", "app_flush",
		"app_background", "app_foreground", "app_low_memory",
		"app_pause", "app_resume", "app_step", "app_speed",
		"app_input", "app_state",
		"app_save_state", "app_restore_state",
		"app_screenshot", "app_log_get", "app_perf_get",
		"app_state_slices", "app_state_describe",
		"app_state_capture_start",
		"app_state_capture_get", "app_state_capture_stop",
		"app_state_capture_list",
	}
	for _, name := range expected {
		_, err := h.Dispatch(name, map[string]any{})
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("dispatcher rejects %q as unknown: %v", name, err)
		}
		_ = context.Background()
	}
}
