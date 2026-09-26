// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package dashboard_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/dashboard"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/rest"
)

func TestDashboard_VerifyTabMarkup(t *testing.T) {
	srv := httptest.NewServer(dashboard.NewHandler())
	t.Cleanup(srv.Close)
	body := httpGet(t, srv.URL+"/")
	if !bytes.Contains(body, []byte("spyder dashboard")) {
		t.Fatal("dashboard page missing")
	}
	for _, want := range []string{
		`data-tab="verify"`,
		`id="tab-verify"`,
		`id="verify-timeline"`,
		`id="verify-gate"`,
		`id="verify-log"`,
		`id="verify-shot"`,
		`id="verify-gate-prompt"`,
		`id="verify-gate-choices"`,
		`id="verify-abort"`,
		`#verify`,
		`verify_status`,
		`verify_answer`,
		`details.group`,
		`gate-sheet`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("dashboard HTML missing %q", want)
		}
	}
	if bytes.Contains(body, []byte(":8765")) {
		t.Error("verify cockpit must not mention a sidecar :8765 listener")
	}
}

func TestDashboard_VerifyDataPath(t *testing.T) {
	h := spydermcp.NewHandler()
	mux := http.NewServeMux()
	mux.Handle(rest.Prefix, rest.NewHandler(h))
	mux.Handle(dashboard.Path, dashboard.NewHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	page := httpGet(t, srv.URL+dashboard.Path)
	if !bytes.Contains(page, []byte(`data-tab="verify"`)) {
		t.Fatal("GET /dashboard missing verify tab")
	}

	wf := `
name: dash-sample
params:
  device: handset
groups:
  - id: look
    label: Looks
steps:
  - id: hello
    type: shell
    group: look
    argv: ["/usr/bin/true"]
  - id: ask
    type: human_gate
    group: look
    prompt: Look
    choices:
      - id: pass
        label: Yes
`
	started := postTool(t, srv.URL, "verify", map[string]any{
		"workflow": wf,
		"cwd":      t.TempDir(),
		"wait":     false,
	})
	runID, _ := started["run_id"].(string)
	if runID == "" {
		t.Fatalf("verify start: %v", started)
	}

	var gate map[string]any
	for i := 0; i < 50; i++ {
		snap := postTool(t, srv.URL, "verify_status", nil)
		if g, ok := snap["gate"].(map[string]any); ok && g["step_id"] == "ask" {
			gate = g
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gate == nil {
		t.Fatal("verify_status never showed the gate sheet payload")
	}
	if gate["workflow"] != "dash-sample" {
		t.Errorf("gate workflow = %v", gate["workflow"])
	}
	if gate["device"] != "handset" {
		t.Errorf("gate device = %v", gate["device"])
	}
	if !strings.Contains(fmtString(gate["prompt"]), "Look") {
		t.Errorf("gate prompt = %v", gate["prompt"])
	}
	choices, _ := gate["choices"].([]any)
	if len(choices) == 0 {
		t.Fatal("gate choices missing (want json tag label/id, not ID/Label)")
	}
	c0, _ := choices[0].(map[string]any)
	if c0["id"] == nil || c0["label"] == nil {
		t.Fatalf("choice json keys = %v (dashboard JS reads id/label)", c0)
	}
	snap := postTool(t, srv.URL, "verify_status", nil)
	runs, _ := snap["runs"].([]any)
	foundGroup := false
	for _, item := range runs {
		rm, _ := item.(map[string]any)
		for _, g := range asSlice(rm["groups"]) {
			gm, _ := g.(map[string]any)
			if gm["id"] == "look" && gm["label"] == "Looks" {
				foundGroup = true
			}
		}
	}
	if !foundGroup {
		t.Fatalf("groups json missing id/label (dashboard collapse); snap=%v", snap)
	}

	ans := postTool(t, srv.URL, "verify_answer", map[string]any{
		"run_id": runID, "gate_id": "ask", "choice_id": "pass",
	})
	if ans["ok"] != true {
		t.Fatalf("verify_answer: %v", ans)
	}
}

func fmtString(v any) string {
	s, _ := v.(string)
	return s
}

func TestDashboard_PathIsDashboardNotSidecar(t *testing.T) {
	if dashboard.Path != "/dashboard" {
		t.Fatalf("Path = %q, want /dashboard", dashboard.Path)
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard#verify", nil)
	rec := httptest.NewRecorder()
	dashboard.NewHandler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /dashboard status %d", rec.Code)
	}
}
