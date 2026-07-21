// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestRunScript_SkeletonBundled(t *testing.T) {
	h := NewHandler()
	res, err := h.handleRunScript(map[string]any{"path": "skeleton"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("error: %v", texts(res))
	}
	joined := strings.Join(texts(res), "\n")
	if !strings.Contains(joined, "skeleton") {
		t.Fatalf("expected skeleton recipe in output: %s", joined)
	}
	if !strings.Contains(joined, `"ok"`) {
		t.Fatalf("expected ok field: %s", joined)
	}
}

func TestAppExec_ScriptPathAndParams(t *testing.T) {
	h := NewHandler()
	res, err := h.handleAppExec(map[string]any{
		"script_path": "skeleton",
		"params":      map[string]any{"session_id": "s-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%v", texts(res))
	}
	joined := strings.Join(texts(res), "\n")
	if !strings.Contains(joined, "s-test") {
		t.Fatalf("params not visible: %s", joined)
	}
}

func TestListScripts(t *testing.T) {
	h := NewHandler()
	res, err := h.handleListScripts(nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(texts(res), "\n")
	if !strings.Contains(joined, "skeleton") {
		t.Fatalf("list missing skeleton: %s", joined)
	}
}

func TestAssertHelpersInStarlark_FailClosed(t *testing.T) {
	// pass
	res := runScript(t, `
pts = [{"x":0.0,"y":0.0},{"x":1.0,"y":0.5}]
assert_trajectory(points=pts, min_x=-1.0, max_x=2.0, min_y=-1.0, max_y=2.0)
emit("traj_ok")
`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("traj pass: %v", texts(res))
	}
	// fail
	res = runScript(t, `
pts = [{"x":0.0,"y":0.0},{"x":99.0,"y":0.0}]
assert_trajectory(points=pts, min_x=-1.0, max_x=2.0, min_y=-1.0, max_y=2.0)
emit("should_not")
`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("expected trajectory fail")
	}
	if !strings.Contains(strings.Join(texts(res), "\n"), "assert_trajectory") {
		t.Fatalf("diagnostic: %v", texts(res))
	}

	res = runScript(t, `
f = [{"x":0.0,"y":0.0},{"x":0.1,"y":0.0}]
o = [{"x":0.0,"y":0.0},{"x":0.5,"y":0.5}]
assert_drag_follow(finger=f, object=o, max_p95=0.05)
`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("expected drag_follow fail")
	}

	res = runScript(t, `
assert_settle(awake=[True, True, False], max_steps=5)
emit("settle_ok")
`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("settle pass: %v", texts(res))
	}
	res = runScript(t, `
assert_settle(awake=[True, True, True], max_steps=5)
`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("expected settle fail when ends awake")
	}
}

func TestL1ResolveInStarlark(t *testing.T) {
	res := runScript(t, `
bodies = {"buggy": {"screen": {"cx": 0.4, "cy": 0.6}}}
node = find_by_label(nodes=bodies, label="buggy")
xy = resolve_target(node=node)
emit(xy)
`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("%v", texts(res))
	}
	joined := strings.Join(texts(res), "\n")
	if !strings.Contains(joined, "0.4") || !strings.Contains(joined, "0.6") {
		t.Fatalf("xy: %s", joined)
	}

	res = runScript(t, `
resolve_target(node={"label": "x"})
`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("expected missing geometry error")
	}
}

func TestFindHitTargetInStarlark(t *testing.T) {
	res := runScript(t, `
payload = {"targets": [
    {"id": "other", "label": "reset", "center_norm": [0.1, 0.1]},
    {"id": "reset", "role": "reset", "label": "TiltBuggy", "center_norm": [0.5, 0.07], "enabled": True},
]}
node = find_hit_target(nodes=payload, id="reset")
xy = resolve_target(node=node)
emit(xy)
emit(node["id"])
`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("%v", texts(res))
	}
	joined := strings.Join(texts(res), "\n")
	if !strings.Contains(joined, "0.5") || !strings.Contains(joined, "0.07") {
		t.Fatalf("xy: %s", joined)
	}
	if !strings.Contains(joined, "reset") {
		t.Fatalf("id: %s", joined)
	}

	// Prefer id over label collision.
	res = runScript(t, `
find_hit_target(nodes=[{"id": "x", "label": "reset", "center_norm": [0,0]}], key="reset")
`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("label must not address hit targets")
	}

	res = runScript(t, `
find_hit_target(nodes=[{"id": "reset", "enabled": False, "center_norm": [0.5, 0.5]}], id="reset")
`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("expected disabled error")
	}
}

func TestRunExec_ParamsGlobal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := runExec(ctx, `emit(params["k"])`, stubVerbs(), health.New(), nil, defaultLim(), map[string]string{"k": "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%v", texts(res))
	}
	if texts(res)[0] != "v1" {
		t.Fatalf("%v", texts(res))
	}
}

func TestRunScript_JSONShape(t *testing.T) {
	h := NewHandler()
	res, err := h.handleListScripts(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Scripts []struct {
			Name string `json:"name"`
		} `json:"scripts"`
	}
	// toolJSON wraps pretty JSON in text content
	raw := texts(res)[0]
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("json: %v body=%s", err, raw)
	}
	if len(payload.Scripts) == 0 {
		t.Fatal("empty scripts")
	}
	_ = mcpgo.CallToolResult{}
}
