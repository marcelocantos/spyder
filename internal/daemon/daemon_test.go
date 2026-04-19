// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mcpTestServer wraps daemon.Build's mux in an httptest.Server; the
// mux already routes /mcp (streamable HTTP) and /api/v1/ (REST).
func mcpTestServer(t *testing.T) (base string, teardown func()) {
	t.Helper()
	handler, _, _ := Build(Config{
		Version:     "test",
		TunneldAddr: "127.0.0.1:1", // guaranteed-unreachable so probe fails quietly
	})
	ts := httptest.NewServer(handler)
	return ts.URL, ts.Close
}

// postJSON sends a POST to url with the given JSON-RPC body and
// returns the parsed response + the mcp-session-id header (empty if
// not present).
func postJSON(t *testing.T, url, session string, body any) (map[string]any, string) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// Streamable HTTP may return either JSON or SSE depending on the
	// Accept header and request type; for a normal JSON-RPC POST the
	// body is plain JSON.
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return out, resp.Header.Get("Mcp-Session-Id")
}

func TestBuild_InitializeRoundtrip(t *testing.T) {
	base, teardown := mcpTestServer(t)
	defer teardown()

	resp, sid := postJSON(t, base+"/mcp", "", map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spyder-test", "version": "1.0"},
		},
	})
	if sid == "" {
		t.Error("expected Mcp-Session-Id header from initialize response")
	}
	result, _ := resp["result"].(map[string]any)
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "spyder" {
		t.Errorf("serverInfo.name = %v; want spyder", serverInfo["name"])
	}
	if serverInfo["version"] != "test" {
		t.Errorf("serverInfo.version = %v; want test", serverInfo["version"])
	}
}

func TestBuild_ToolsListHasAllTools(t *testing.T) {
	base, teardown := mcpTestServer(t)
	defer teardown()

	// Initialize first (required to open a session).
	_, sid := postJSON(t, base+"/mcp", "", map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "t", "version": "1"},
		},
	})

	// tools/list within the session.
	resp, _ := postJSON(t, base+"/mcp", sid, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})

	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := map[string]bool{}
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		if n, ok := tm["name"].(string); ok {
			names[n] = true
		}
	}
	for _, want := range []string{
		"devices", "resolve", "keepawake", "device_state",
		"screenshot", "list_apps", "launch_app", "terminate_app",
	} {
		if !names[want] {
			t.Errorf("tools/list missing %q; got %v", want, names)
		}
	}
}

func TestRun_ShutsDownOnContextCancel(t *testing.T) {
	// Verifies Run returns promptly after context cancellation
	// (the happy-path for signal-driven shutdown). Uses ":0" so the
	// kernel picks a free port. DisableAutoAwake keeps the test from
	// poking real tunneld.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			Addr:             ":0",
			Version:          "test",
			TunneldAddr:      "127.0.0.1:1",
			DisableAutoAwake: true,
		})
	}()

	// Give the server a moment to bind.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Either a clean shutdown (nil) or "use of closed network
		// connection" bubbling up from http.Server. Both are fine;
		// the point is that Run returned.
		if err != nil && !strings.Contains(err.Error(), "closed") {
			t.Errorf("Run returned unexpected err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run did not return within 3s after ctx cancel")
	}
}
