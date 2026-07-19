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
		case appchannel.MethodSensorControl:
			var p map[string]any
			_ = appchannel.UnpackParams(env.Params, &p)
			mode, _ := p["mode"].(string)
			if mode == "" {
				mode = "passthrough"
			}
			result = map[string]any{"sensor": "accel", "mode": mode}
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
	t.Cleanup(h.appChannel.Close)
	return h
}

// testAppKey returns a stable AppKey for tests that don't otherwise
// care about device/bundle identity.
func testAppKey() appchannel.AppKey {
	return appchannel.AppKey{DeviceID: "test-device", BundleID: "com.example.smoke"}
}

func openListener(t *testing.T, h *Handler) (listenerID string, port int) {
	t.Helper()
	l, err := h.appChannel.GetOrCreateListener(testAppKey())
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	return l.ID, l.Port
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

func TestAppChannel_ListAndStop(t *testing.T) {
	h := startAppChannelHandler(t)
	id, port := openListener(t, h)
	if port == 0 {
		t.Fatal("port = 0")
	}

	// list should show the keyed listener (no session yet because no app connected).
	r := dispatchJSON(t, h, "app_channel_list", nil)
	if r.IsError {
		t.Fatalf("app_channel_list: %s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), id) {
		t.Errorf("list did not contain listener_id %s: %s", id, resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "test-device") {
		t.Errorf("list did not include device_id: %s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "idle_since") {
		t.Errorf("listener with no session should report idle_since: %s", resultText(t, &r))
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
		appchannel.MethodInputInject, appchannel.MethodSensorControl,
		appchannel.MethodStateQuery,
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
		{"app_sensor_control", map[string]any{"session_id": sessionID, "sensor": "accel", "mode": "override", "x": 1.0, "y": 0.0, "z": 0.0}, func(t *testing.T, body string) {
			if !strings.Contains(body, "override") {
				t.Errorf("sensor_control body: %s", body)
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

// app_channel_start was removed in T83 (auto-managed listeners) — the
// dispatcher should now refuse it as an unknown tool.
func TestAppChannel_StartRemoved(t *testing.T) {
	h := startAppChannelHandler(t)
	_, err := h.Dispatch(context.Background(), "app_channel_start", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("Dispatch(app_channel_start) err = %v; want unknown tool", err)
	}
}

// Resolving an app_* call by (device, bundle_id) when session_id is
// omitted is the headline T83 ergonomic upgrade. The keyed registry
// stores under the resolved device UUID, so this test mirrors
// what the handler will do at lookup time.
func TestAppChannel_ResolveSessionByDeviceAndBundleID(t *testing.T) {
	h := startAppChannelHandler(t)
	const iPadUUID = "00008103-001122334455667A"
	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{
		DeviceID: iPadUUID, BundleID: "com.smoke.app",
	})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	client := dialSmoke(t, l.Port, []string{appchannel.MethodPing})
	defer client.close()
	_ = waitForAppSession(t, h)

	// Address by alias — handler resolves "iPad" → iPadUUID before lookup.
	r := dispatchJSON(t, h, "app_ping", map[string]any{
		"device": "iPad", "bundle_id": "com.smoke.app",
	})
	if r.IsError {
		t.Fatalf("ping by (device, bundle_id) should succeed: %s", resultText(t, &r))
	}

	r = dispatchJSON(t, h, "app_ping", map[string]any{
		"device": "iPad", "bundle_id": "com.other.app",
	})
	if !r.IsError {
		t.Fatal("ping for unknown bundle_id should error")
	}
}

// GetOrCreateListener returns the same listener (and port) on a
// repeat call for the same (device, bundle_id) — that's the listener-
// reuse promise the auto-managed model rests on.
func TestAppChannel_ListenerReuseSamePort(t *testing.T) {
	h := startAppChannelHandler(t)
	key := appchannel.AppKey{DeviceID: "iPad", BundleID: "com.app"}
	first, err := h.appChannel.GetOrCreateListener(key)
	if err != nil {
		t.Fatalf("first GetOrCreateListener: %v", err)
	}
	second, err := h.appChannel.GetOrCreateListener(key)
	if err != nil {
		t.Fatalf("second GetOrCreateListener: %v", err)
	}
	if first.ID != second.ID || first.Port != second.Port {
		t.Errorf("second call should return same listener; first=(%s,%d) second=(%s,%d)",
			first.ID, first.Port, second.ID, second.Port)
	}
}

// The idle reaper drops a keyed listener with zero live sessions
// once its lastTouched is older than KeyedListenerIdleTTL. Tests
// shrink the TTL so the wall-clock cost is microseconds.
func TestAppChannel_IdleReaperDropsStaleListener(t *testing.T) {
	prevTTL := appchannel.KeyedListenerIdleTTL
	appchannel.KeyedListenerIdleTTL = time.Millisecond
	t.Cleanup(func() { appchannel.KeyedListenerIdleTTL = prevTTL })

	h := startAppChannelHandler(t)
	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{
		DeviceID: "iPad", BundleID: "com.stale.app",
	})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	h.appChannel.ReapIdleKeyedListeners()

	if _, ok := h.appChannel.LookupKeyed(l.Key); ok {
		t.Errorf("stale listener for %v should have been reaped", l.Key)
	}
}

// A keyed listener with a live session is NOT reaped — even when
// lastTouched is older than the TTL — because the agent is still
// observing the app.
func TestAppChannel_IdleReaperKeepsLiveSession(t *testing.T) {
	prevTTL := appchannel.KeyedListenerIdleTTL
	appchannel.KeyedListenerIdleTTL = time.Millisecond
	t.Cleanup(func() { appchannel.KeyedListenerIdleTTL = prevTTL })

	h := startAppChannelHandler(t)
	key := appchannel.AppKey{DeviceID: "iPad", BundleID: "com.live.app"}
	l, err := h.appChannel.GetOrCreateListener(key)
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	client := dialSmoke(t, l.Port, []string{appchannel.MethodPing})
	defer client.close()
	_ = waitForAppSession(t, h)

	time.Sleep(5 * time.Millisecond)
	h.appChannel.ReapIdleKeyedListeners()

	if _, ok := h.appChannel.LookupKeyed(key); !ok {
		t.Errorf("listener with live session should not be reaped")
	}
}

// Listener.Stop must complete promptly even when a connected peer
// vanishes without a clean TCP close (the iOS test case that wedged
// the installed spyder for ~4.8h). The deadlock was: Session.Close
// waited on `<-s.done`, but s.done is closed by readLoop, which was
// parked in a blocking ReadFrame that ctx cancellation can't
// interrupt. Fix: close s.conn directly in Session.Close.
func TestAppChannel_StopDoesNotDeadlockOnSilentPeer(t *testing.T) {
	h := startAppChannelHandler(t)
	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{
		DeviceID: "iPad", BundleID: "com.silent.app",
	})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	// Dial + complete the handshake so a Session is registered, then
	// hold the conn open without ever sending or closing it — the
	// readLoop will be parked in ReadFrame.
	client := dialSmoke(t, l.Port, []string{appchannel.MethodPing})
	defer client.close()
	_ = waitForAppSession(t, h)

	done := make(chan struct{})
	go func() {
		l.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Listener.Stop deadlocked — Session.Close failed to unblock readLoop")
	}
}

// Sanity: the daemon dispatcher recognises every new tool.
func TestAppChannel_DispatchSurfaceCoverage(t *testing.T) {
	h := startAppChannelHandler(t)
	expected := []string{
		"app_channel_stop", "app_channel_list",
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
		_, err := h.Dispatch(context.Background(), name, map[string]any{})
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("dispatcher rejects %q as unknown: %v", name, err)
		}
		_ = context.Background()
	}
}
