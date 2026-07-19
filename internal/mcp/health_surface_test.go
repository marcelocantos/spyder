// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Oracle for 🎯T90.3: the three health surfaces (REST GET /api/v1/health,
// the health() app_exec builtin, and — by construction, since it is the REST
// client — `spyder status`) must all report the SAME underlying model
// snapshot. This test injects synthetic state through the model seams, reads
// it back through REST and through app_exec, and asserts the entity
// kind/name/state sets are identical to model.Snapshot(). It lives in the
// external mcp_test package so it can import internal/rest without the
// import cycle a same-package test would create (rest imports mcp).
package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcelocantos/spyder/internal/health"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/rest"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// entityKey is the (kind, name, layer, state) tuple used to compare the three
// surfaces. Attempts/evidence are excluded: the acceptance is that the same
// entities in the same states are reported, not byte-identical timestamps.
type entityKey struct {
	Kind, Name, Layer, State string
}

// injectSyntheticHealth drives the model into a known three-entity shape via
// the public seams: a healthy daemon, a degraded subprocess, an absent
// device. Returns the expected key set for comparison.
func injectSyntheticHealth(m *health.Model) map[entityKey]bool {
	daemonID := health.ID{Kind: health.KindDaemon, Name: "spyderd"}
	m.Register(daemonID, health.KindDaemon, health.Policy{})
	m.Observe(daemonID, true, "serving")

	subID := health.ID{Kind: health.KindSubprocess, Name: "ios-tunnel"}
	m.Register(subID, health.KindSubprocess, health.Policy{MaxAttempts: 3})
	m.Observe(subID, false, "process exited") // Healthy -> Degraded

	devID := health.ID{Kind: health.KindDevice, Name: "iPad", Layer: "usbmux"}
	m.MarkAbsent(devID, false, "unplugged") // -> AbsentUnexpected

	return map[entityKey]bool{
		{"daemon", "spyderd", "", "healthy"}:              true,
		{"subprocess", "ios-tunnel", "", "degraded"}:      true,
		{"device", "iPad", "usbmux", "absent_unexpected"}: true,
	}
}

// keysFromSnapshot reduces a snapshot to the comparison key set.
func keysFromSnapshot(snap health.Snapshot) map[entityKey]bool {
	out := make(map[entityKey]bool, len(snap.Entities))
	for _, e := range snap.Entities {
		out[entityKey{string(e.Kind), e.ID.Name, e.ID.Layer, string(e.State)}] = true
	}
	return out
}

// TestHealthSurfaces_AgreeWithModel is the class-1 oracle: REST ≡ builtin ≡
// model. CLI ≡ REST by construction (status is the REST client), so verifying
// REST covers it.
func TestHealthSurfaces_AgreeWithModel(t *testing.T) {
	h := spydermcp.NewHandler()
	m := h.Health().Model()
	want := injectSyntheticHealth(m)

	// The model is the reference.
	modelKeys := keysFromSnapshot(m.Snapshot())
	if !keysEqual(want, modelKeys) {
		t.Fatalf("model snapshot keys = %v; want %v", modelKeys, want)
	}

	// --- Surface 1: REST GET /api/v1/health ---
	ts := httptest.NewServer(rest.NewHandler(h))
	defer ts.Close()

	resp, err := http.Get(ts.URL + rest.HealthPath)
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET health status = %d; want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)

	// (3) REST body is HealthReport: Snapshot fields + doctor_finding + in_flight.
	var restRep spydermcp.HealthReport
	if err := json.Unmarshal(body, &restRep); err != nil {
		t.Fatalf("REST body is not a valid HealthReport: %v (body: %s)", err, body)
	}
	restKeys := keysFromSnapshot(restRep.Snapshot())
	if !keysEqual(want, restKeys) {
		t.Errorf("REST keys = %v; want %v", restKeys, want)
	}
	// doctor_finding is a struct value (always present); in_flight may be empty.
	_ = restRep.DoctorFinding
	if restRep.InFlight == nil {
		// encode as empty slice, not null
		t.Log("in_flight is nil (acceptable if empty); prefer []")
	}

	// REST is GET-only: a POST must be rejected (health is pull-only).
	pr, err := http.Post(ts.URL+rest.HealthPath, "application/json", nil)
	if err != nil {
		t.Fatalf("POST health: %v", err)
	}
	pr.Body.Close()
	if pr.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST health status = %d; want 405", pr.StatusCode)
	}

	// --- Surface 2: health() app_exec builtin ---
	res, err := h.Dispatch(context.Background(), "app_exec", map[string]any{"script": "emit(health())"})
	if err != nil {
		t.Fatalf("app_exec dispatch: %v", err)
	}
	if res.IsError {
		t.Fatalf("app_exec returned error: %s", firstText(res))
	}
	builtinKeys := keysFromExecResult(t, firstText(res))
	if !keysEqual(want, builtinKeys) {
		t.Errorf("health() builtin keys = %v; want %v", builtinKeys, want)
	}
}

// keysFromExecResult parses the JSON the health() builtin emits (the flat
// {kind,name,layer,state,...} entity shape) into the comparison key set.
func keysFromExecResult(t *testing.T, text string) map[entityKey]bool {
	t.Helper()
	var out struct {
		Entities []struct {
			Kind  string `json:"kind"`
			Name  string `json:"name"`
			Layer string `json:"layer"`
			State string `json:"state"`
		} `json:"entities"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("health() builtin output is not JSON: %v (text: %s)", err, text)
	}
	keys := make(map[entityKey]bool, len(out.Entities))
	for _, e := range out.Entities {
		keys[entityKey{e.Kind, e.Name, e.Layer, e.State}] = true
	}
	return keys
}

func keysEqual(a, b map[entityKey]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// firstText returns the first text content block of a CallToolResult, or "".
func firstText(res *mcpgo.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
