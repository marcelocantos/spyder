// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package dashboard_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/marcelocantos/spyder/internal/dashboard"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/rest"
	"github.com/marcelocantos/spyder/internal/verify"
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
		`id="verify-runs"`,
		`el.className = "verify-run"`,
		`class="verify-grid"`,
		`data-v="timeline"`,
		`data-v="gate"`,
		`data-v="log"`,
		`data-v="shot"`,
		`data-v="gate-prompt"`,
		`data-v="gate-choices"`,
		`data-v="abort"`,
		`#verify`,
		`/ws/verify`,
		`id="verify-badge"`,
		`tab-count-blink`,
		`startVerifyWS`,
		`blinkVerifyBadge`,
		`verify_answer`,
		`details.group`,
		`gate-sheet`,
		`run.screenshot || (gate && gate.screenshot)`,
		`id="verify-tabs"`,
		`renderVerifyTabs`,
		`data-v="detail"`,
		`verify_detail`,
		`gate.run_id !== verifySelected`,
		`card.comment.value = ""`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("dashboard HTML missing %q", want)
		}
	}
	for _, forbid := range []string{
		`id="verify-refresh"`,
		`startVerifyPoll`,
		`verifyTimer`,
		`setInterval(tick, 1000)`,
	} {
		if bytes.Contains(body, []byte(forbid)) {
			t.Errorf("dashboard HTML must not poll verify: still contains %q", forbid)
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
	t.Cleanup(func() {
		_ = h.VerifyHub().Abort(runID, "test done")
		if r := h.VerifyHub().RunByID(runID); r != nil {
			r.Wait()
		}
	})

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

func TestDashboard_VerifyScreenshotOnStatus(t *testing.T) {
	h := spydermcp.NewHandler()
	mux := http.NewServeMux()
	mux.Handle(rest.Prefix, rest.NewHandler(h))
	mux.Handle(dashboard.Path, dashboard.NewHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cwd := t.TempDir()
	seed := writeDashPNG(t, filepath.Join(cwd, "seed.png"))
	wf := `
name: shot-dash
steps:
  - id: snap
    type: shell
    command: cp seed.png "$SPYDER_VERIFY_ARTIFACT_DIR/screenshots/live.png"
  - id: ask
    type: human_gate
    requires: ["snap"]
    prompt: Look
    choices:
      - id: pass
        label: Yes
`
	started := postTool(t, srv.URL, "verify", map[string]any{
		"workflow": wf, "cwd": cwd, "wait": false,
	})
	runID, _ := started["run_id"].(string)
	if runID == "" {
		t.Fatalf("verify start: %v", started)
	}
	t.Cleanup(func() {
		_ = h.VerifyHub().Abort(runID, "test done")
		if r := h.VerifyHub().RunByID(runID); r != nil {
			r.Wait()
		}
	})

	var runShot, gateShot string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snap := postTool(t, srv.URL, "verify_status", nil)
		gate, _ := snap["gate"].(map[string]any)
		if gate != nil && gate["step_id"] == "ask" {
			gateShot = fmtString(gate["screenshot"])
		}
		for _, item := range asSlice(snap["runs"]) {
			rm, _ := item.(map[string]any)
			if rm["run_id"] == runID {
				runShot = fmtString(rm["screenshot"])
			}
		}
		if runShot != "" && gateShot != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runShot == "" || gateShot == "" {
		t.Fatalf("verify_status missing screenshot run=%q gate=%q", runShot, gateShot)
	}
	assertDashShot(t, runShot, seed)
	assertDashShot(t, gateShot, seed)

	ans := postTool(t, srv.URL, "verify_answer", map[string]any{
		"run_id": runID, "gate_id": "ask", "choice_id": "pass",
	})
	if ans["ok"] != true {
		t.Fatalf("verify_answer: %v", ans)
	}
}

func writeDashPNG(t *testing.T, path string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.RGBA{R: 0x44, G: 0x55, B: 0x66, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertDashShot(t *testing.T, uri string, seed []byte) {
	t.Helper()
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("screenshot %q is not a png data URI (raw paths are not browser-displayable)", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatalf("decode screenshot: %v", err)
	}
	if !bytes.Equal(raw, seed) {
		t.Fatalf("screenshot bytes do not match the PNG the shell wrote")
	}
}

func TestDashboard_VerifyWSPushesRun(t *testing.T) {
	h := spydermcp.NewHandler()
	mux := http.NewServeMux()
	mux.Handle(rest.Prefix, rest.NewHandler(h))
	mux.Handle(dashboard.Path, dashboard.NewHandler())
	mux.HandleFunc(verify.WSPath, h.VerifyHub().HandleWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	t.Cleanup(cancel)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + verify.WSPath
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })

	if _, data, err := c.Read(ctx); err != nil {
		t.Fatalf("first snapshot: %v", err)
	} else {
		var snap map[string]any
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatalf("first snapshot json: %v", err)
		}
		if runs, _ := snap["runs"].([]any); len(runs) != 0 {
			t.Fatalf("fresh snapshot runs = %v", runs)
		}
	}

	wf := `
name: dash-ws
steps:
  - id: ask
    type: human_gate
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
	t.Cleanup(func() {
		_ = h.VerifyHub().Abort(runID, "test done")
		if r := h.VerifyHub().RunByID(runID); r != nil {
			r.Wait()
		}
	})

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for run on websocket (REST was not polled): %v", err)
		}
		var snap map[string]any
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatalf("snapshot json: %v", err)
		}
		for _, item := range asSlice(snap["runs"]) {
			rm, _ := item.(map[string]any)
			if rm["run_id"] == runID && rm["status"] == "running" {
				return
			}
		}
	}
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
