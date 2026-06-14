// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

// dialSliceApp connects to a spyder appchannel listener with a hello
// that advertises state_query support plus the given slice catalogue,
// then responds to state_query calls with a counter-bumping payload
// so capture polling sees distinguishable samples per tick.
func dialSliceApp(t *testing.T, port int, slices []string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	descs := make([]appchannel.SliceDescriptor, len(slices))
	for i, name := range slices {
		descs[i] = appchannel.SliceDescriptor{Name: name}
	}
	helloParams, _ := appchannel.PackParams(appchannel.Hello{
		AppName:    "slice-smoke",
		AppVersion: "test",
		Methods:    []string{appchannel.MethodPing, appchannel.MethodStateQuery},
		Slices:     descs,
	})
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if _, err := appchannel.ReadFrame(conn); err != nil {
		t.Fatalf("hello ack: %v", err)
	}
	var tick int
	go func() {
		for {
			env, err := appchannel.ReadFrame(conn)
			if err != nil {
				return
			}
			if !env.IsRequest() {
				continue
			}
			if env.Method == appchannel.MethodStateQuery {
				tick++
				var p struct {
					Slice string `msgpack:"slice"`
				}
				_ = appchannel.UnpackParams(env.Params, &p)
				raw, _ := appchannel.PackParams(map[string]any{
					"slice": p.Slice,
					"tick":  tick,
				})
				_ = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: env.ID, Result: raw})
				continue
			}
			raw, _ := appchannel.PackParams(map[string]bool{"ok": true})
			_ = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: env.ID, Result: raw})
		}
	}()
	return conn
}

func TestAppStateSlices_ReturnsHelloCatalogue(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSliceApp(t, port, []string{"scene", "physics", "hud"})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_state_slices", nil)
	if r.IsError {
		t.Fatalf("app_state_slices: %s", resultText(t, &r))
	}
	body := resultText(t, &r)

	var resp struct {
		Slices []appchannel.SliceDescriptor `json:"slices"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Slices) != 3 {
		t.Errorf("slices = %v; want 3 entries", resp.Slices)
	}
	for i, want := range []string{"scene", "physics", "hud"} {
		if resp.Slices[i].Name != want {
			t.Errorf("Slices[%d].Name = %q; want %q", i, resp.Slices[i].Name, want)
		}
	}
}

func TestAppStateSlices_EmptyWhenAppOmits(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSliceApp(t, port, nil) // no slices in hello
	defer conn.Close()
	_ = waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_state_slices", nil)
	body := resultText(t, &r)
	if !strings.Contains(body, `"slices": []`) && !strings.Contains(body, `"slices":[]`) {
		t.Errorf("expected empty slices array; got %s", body)
	}
}

func TestAppState_WithSelect(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSliceApp(t, port, []string{"physics"})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	// Filter to extract just the tick number from the slice response.
	r := dispatchJSON(t, h, "app_state", map[string]any{"slice": "physics", "select": ".tick"})
	if r.IsError {
		t.Fatalf("app_state with select: %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "1") {
		t.Errorf("expected tick=1 in body; got %s", body)
	}
}

func TestAppState_SelectErrorSurfaces(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSliceApp(t, port, []string{"scene"})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_state", map[string]any{"slice": "scene", "select": ".[bad"})
	body := resultText(t, &r)
	if !strings.Contains(body, "select_error") {
		t.Errorf("expected select_error in body; got %s", body)
	}
}

func TestAppStateDescribe(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSliceApp(t, port, []string{"scene"})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_state_describe", map[string]any{"slice": "scene"})
	if r.IsError {
		t.Fatalf("app_state_describe: %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	// dialSliceApp's payload is `{slice: "...", tick: N}` — describe
	// should emit `{slice: "string", tick: "int"}`.
	if !strings.Contains(body, `"slice": "string"`) {
		t.Errorf("expected slice:string in shape; got %s", body)
	}
	if !strings.Contains(body, `"tick": "int"`) {
		t.Errorf("expected tick:int in shape; got %s", body)
	}
}

func TestAppStateCapture_StartGetStopRoundTrip(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSliceApp(t, port, []string{"scene"})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	// Start.
	r := dispatchJSON(t, h, "app_state_capture_start", map[string]any{
		"slice":       "scene",
		"interval_ms": 30.0,
	})
	if r.IsError {
		t.Fatalf("app_state_capture_start: %s", resultText(t, &r))
	}
	var startResp struct {
		CaptureID string `json:"capture_id"`
		Slice     string `json:"slice"`
	}
	if err := json.Unmarshal([]byte(resultText(t, &r)), &startResp); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if startResp.CaptureID == "" || startResp.Slice != "scene" {
		t.Errorf("start resp = %+v", startResp)
	}

	// Wait for samples to land.
	time.Sleep(150 * time.Millisecond)

	// Get drains.
	r = dispatchJSON(t, h, "app_state_capture_get", map[string]any{"capture_id": startResp.CaptureID})
	if r.IsError {
		t.Fatalf("app_state_capture_get: %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if !strings.Contains(body, `"tick"`) {
		t.Errorf("body missing tick field: %s", body)
	}

	// List shows the active capture.
	r = dispatchJSON(t, h, "app_state_capture_list", nil)
	if r.IsError {
		t.Fatalf("app_state_capture_list: %s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), startResp.CaptureID) {
		t.Errorf("list missing capture_id %q: %s", startResp.CaptureID, resultText(t, &r))
	}

	// Stop tears down.
	r = dispatchJSON(t, h, "app_state_capture_stop", map[string]any{"capture_id": startResp.CaptureID})
	if r.IsError {
		t.Fatalf("app_state_capture_stop: %s", resultText(t, &r))
	}

	// Second stop errors.
	r = dispatchJSON(t, h, "app_state_capture_stop", map[string]any{"capture_id": startResp.CaptureID})
	if !r.IsError {
		t.Error("second stop should error")
	}
}
