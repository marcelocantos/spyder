// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func dialSequencedSliceApp(t *testing.T, port int, slice string, payloads []map[string]any) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	helloParams, _ := appchannel.PackParams(appchannel.Hello{
		AppName:    "wait-smoke",
		AppVersion: "test",
		Methods:    appchannel.MethodDescriptors(appchannel.MethodPing, appchannel.MethodStateQuery),
		Slices:     []appchannel.SliceDescriptor{{Name: slice}},
	})
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if _, err := appchannel.ReadFrame(conn); err != nil {
		t.Fatalf("hello ack: %v", err)
	}
	var n atomic.Int32
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
				i := int(n.Add(1) - 1)
				if i >= len(payloads) {
					i = len(payloads) - 1
				}
				raw, _ := appchannel.PackParams(payloads[i])
				_ = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: env.ID, Result: raw})
				continue
			}
			raw, _ := appchannel.PackParams(map[string]bool{"ok": true})
			_ = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: env.ID, Result: raw})
		}
	}()
	return conn
}

func TestWaitState_HandlerFalseThenTrue(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSequencedSliceApp(t, port, "carousel", []map[string]any{
		{"present": false, "active": false, "tick": 1},
		{"present": true, "active": true, "tick": 2, "layout_offset": 3.0},
	})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	r := dispatchJSON(t, h, "wait_state", map[string]any{
		"slice":      "carousel",
		"select":     "select(.present == true and .active == true)",
		"timeout_ms": 2000.0,
		"poll_ms":    10.0,
	})
	if r.IsError {
		t.Fatalf("wait_state: %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	var resp struct {
		Value map[string]any `json:"value"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if resp.Value["tick"] != float64(2) && fmt.Sprint(resp.Value["tick"]) != "2" {
		t.Errorf("value=%v; want tick 2", resp.Value)
	}
}

func TestWaitState_HandlerTimeoutIncludesLastObserved(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSequencedSliceApp(t, port, "carousel", []map[string]any{
		{"present": false, "screen": "menu", "tick": 1},
	})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	r := dispatchJSON(t, h, "wait_state", map[string]any{
		"slice":      "carousel",
		"select":     "select(.present == true)",
		"timeout_ms": 80.0,
		"poll_ms":    15.0,
	})
	if !r.IsError {
		t.Fatal("expected timeout")
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "last observed") {
		t.Errorf("timeout must include last observed: %s", body)
	}
	if !strings.Contains(body, "menu") && !strings.Contains(body, "present") {
		t.Errorf("last slice missing: %s", body)
	}
	if body == "timed out" {
		t.Fatal("bare timed out")
	}
}

func TestWaitState_AlreadyTrue(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	conn := dialSequencedSliceApp(t, port, "session", []map[string]any{
		{"screen": "customize_decals", "ready": true},
	})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	r := dispatchJSON(t, h, "wait_state", map[string]any{
		"slice":      "session",
		"select":     ".ready",
		"timeout_ms": 1000.0,
		"poll_ms":    50.0,
	})
	if r.IsError {
		t.Fatalf("already-true: %s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "true") {
		t.Errorf("body=%s", resultText(t, &r))
	}
}
