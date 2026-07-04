// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package gedharness is a lightweight differential harness for the
// ged↔spyder migration: it records a golden corpus of ged's HTTP
// responses, normalizes non-deterministic fields (PIDs, session IDs,
// ordering), and structurally diffs a candidate (spyder's app-channel)
// against the golden. It is a corpus + a differ, not a framework.
//
// ged is the runnable reference — a Go dev daemon run headlessly as
// `ged --no-open --port <p>`. Client speaks its plain HTTP+JSON surface.
// Confirmed routes (ge repo, ged/dashboard.go):
//
//	GET  /api/info          — {connected, servers[{id,name,pid,sessions}], sessions}
//	GET  /api/tweaks        — cached "tweaks" state (array; [] when no app)
//	POST /api/tweaks        — tweak_set; body {name,value} forwarded as
//	                          {"type":"tweak_set","data":<body>}
//	POST /api/tweaks/reset  — tweak_reset; body {name} or {all:true}
//
// There is NO plain-HTTP logs route: ged exposes recent log history only
// over the /ws/logs WebSocket and the /mcp `logs` tool. Logs therefore
// returns a not-available error rather than guessing a route.
package gedharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// requestTimeout bounds every ged HTTP call. ged is a local daemon, so a
// few seconds is generous; a stuck request should fail fast, not hang the
// recorder.
const requestTimeout = 5 * time.Second

// ErrLogsUnavailable reports that ged has no plain-HTTP logs route. Log
// history is only reachable over the /ws/logs WebSocket or the /mcp
// `logs` tool, neither of which this HTTP client speaks.
var ErrLogsUnavailable = errors.New("gedharness: logs not available over HTTP (ged exposes log history only via the /ws/logs WebSocket and the /mcp logs tool)")

// Client is a minimal HTTP client for a running ged daemon.
type Client struct {
	baseURL string
	hc      *http.Client
}

// NewClient returns a Client targeting a ged daemon at baseURL
// (e.g. "http://localhost:42069"). A trailing slash is tolerated.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: trimTrailingSlash(baseURL),
		hc:      &http.Client{Timeout: requestTimeout},
	}
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// Info returns GET /api/info: ged's connection status, connected
// servers, and active-session count.
func (c *Client) Info(ctx context.Context) (json.RawMessage, error) {
	return c.getJSON(ctx, "/api/info")
}

// Tweaks returns GET /api/tweaks: the last cached tweak state (an array;
// [] when no app is connected).
func (c *Client) Tweaks(ctx context.Context) (json.RawMessage, error) {
	return c.getJSON(ctx, "/api/tweaks")
}

// Logs would return recent log entries, but ged has no plain-HTTP logs
// route (see ErrLogsUnavailable). count is accepted for signature
// symmetry with the WebSocket/MCP surface but is unused.
func (c *Client) Logs(ctx context.Context, count int) (json.RawMessage, error) {
	_ = count
	return nil, ErrLogsUnavailable
}

// TweakSet sets a tweak via POST /api/tweaks. ged wraps the body as
// {"type":"tweak_set","data":{name,value}} and forwards it to the
// connected game server; with no server attached it returns 503, which
// surfaces here as a non-2xx error.
func (c *Client) TweakSet(ctx context.Context, name string, value any) error {
	body, err := json.Marshal(map[string]any{"name": name, "value": value})
	if err != nil {
		return fmt.Errorf("gedharness: marshal tweak_set body: %w", err)
	}
	_, err = c.postJSON(ctx, "/api/tweaks", body)
	return err
}

// TweakReset resets a tweak (or all tweaks when name=="") via
// POST /api/tweaks/reset. ged wraps the body as
// {"type":"tweak_reset","data":{name}} or {..."data":{all:true}}.
func (c *Client) TweakReset(ctx context.Context, name string) error {
	var body []byte
	var err error
	if name == "" {
		// Empty name means reset everything: ged's own MCP tool sends
		// {"all":true} for this case.
		body = []byte(`{"all":true}`)
	} else {
		body, err = json.Marshal(map[string]string{"name": name})
		if err != nil {
			return fmt.Errorf("gedharness: marshal tweak_reset body: %w", err)
		}
	}
	_, err = c.postJSON(ctx, "/api/tweaks/reset", body)
	return err
}

func (c *Client) getJSON(ctx context.Context, path string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("gedharness: build GET %s: %w", path, err)
	}
	return c.do(req, path)
}

func (c *Client) postJSON(ctx context.Context, path string, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gedharness: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, path)
}

func (c *Client) do(req *http.Request, path string) (json.RawMessage, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gedharness: %s %s: %w", req.Method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gedharness: read %s %s body: %w", req.Method, path, err)
	}
	// Non-2xx is an error; include the status line and (trimmed) body so
	// the caller can see ged's own error text (e.g. "no server connected").
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gedharness: %s %s: status %s: %s", req.Method, path, resp.Status, bytes.TrimSpace(data))
	}
	return json.RawMessage(data), nil
}
