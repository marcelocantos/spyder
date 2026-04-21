// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package rest_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/rest"
)

func newTestServer(t *testing.T) (string, func()) {
	t.Helper()
	// NewHandler with no options is safe for the tools this suite exercises
	// (reservations, unknown-tool dispatch). The REST handler is transport-only;
	// the underlying mcp.Handler does the real work.
	h := spydermcp.NewHandler()
	ts := httptest.NewServer(rest.NewHandler(h))
	return ts.URL, ts.Close
}

// TestReservations_HappyPath verifies a zero-arg tool reachable via
// REST returns a JSON body shaped like mcp.CallToolResult.
func TestReservations_HappyPath(t *testing.T) {
	base, teardown := newTestServer(t)
	defer teardown()

	resp, err := http.Post(base+rest.Prefix+"reservations",
		"application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if len(out.Content) == 0 || out.Content[0].Type != "text" {
		t.Fatalf("unexpected content: %q", raw)
	}
	// No reservations configured on the handler — empty JSON array.
	if strings.TrimSpace(out.Content[0].Text) != "[]" {
		t.Errorf("content.text = %q; want []", out.Content[0].Text)
	}
}

// TestUnknownTool_Returns404 verifies POST /api/v1/<bogus> is a 404.
func TestUnknownTool_Returns404(t *testing.T) {
	base, teardown := newTestServer(t)
	defer teardown()

	resp, err := http.Post(base+rest.Prefix+"nosuchtool",
		"application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d; want 404", resp.StatusCode)
	}
}

// TestWrongMethod_Returns405 verifies GET is rejected with 405.
func TestWrongMethod_Returns405(t *testing.T) {
	base, teardown := newTestServer(t)
	defer teardown()

	resp, err := http.Get(base + rest.Prefix + "reservations")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q; want POST", got)
	}
}

// TestBadJSON_Returns400 verifies malformed bodies are a 400.
func TestBadJSON_Returns400(t *testing.T) {
	base, teardown := newTestServer(t)
	defer teardown()

	resp, err := http.Post(base+rest.Prefix+"reservations",
		"application/json", strings.NewReader("{not-json"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

// TestSubpath_Returns404 ensures /api/v1/tool/extra is rejected rather
// than interpreted as "tool/extra".
func TestSubpath_Returns404(t *testing.T) {
	base, teardown := newTestServer(t)
	defer teardown()

	resp, err := http.Post(base+rest.Prefix+"reservations/oops",
		"application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d; want 404", resp.StatusCode)
	}
}

// TestReserve_ArgsRoundtrip verifies a tool with required args still
// returns a structured mcp.CallToolResult with isError=true on missing
// arguments (rather than transport-level 400 — arg validation is the
// tool's job, not REST's).
func TestReserve_ArgsRoundtrip(t *testing.T) {
	base, teardown := newTestServer(t)
	defer teardown()

	body, _ := json.Marshal(map[string]any{}) // no owner, no device
	req, _ := http.NewRequest("POST",
		base+rest.Prefix+"reserve",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	// Missing arguments are a tool error; REST passes them through.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200 (tool error in body)", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if !out.IsError {
		t.Errorf("expected isError=true for missing args; got %q", raw)
	}
}
