// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"

	"github.com/marcelocantos/spyder/internal/scriptlib"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"go.starlark.net/starlark"
)

// --- 🎯T108 pure helpers exposed as Starlark builtins -----------------

func builtinAssertTrajectory(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var points starlark.Value
	var minX, maxX, minY, maxY float64
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"points", &points,
		"min_x", &minX,
		"max_x", &maxX,
		"min_y", &minY,
		"max_y", &maxY,
	); err != nil {
		return nil, err
	}
	pts, err := pointSeries(points)
	if err != nil {
		return nil, fmt.Errorf("assert_trajectory: %w", err)
	}
	if err := scriptlib.AssertTrajectoryCorridor(pts, minX, maxX, minY, maxY); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func builtinAssertDragFollow(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var fingerV, objectV starlark.Value
	var maxP95 float64
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"finger", &fingerV,
		"object", &objectV,
		"max_p95", &maxP95,
	); err != nil {
		return nil, err
	}
	finger, err := pointSeries(fingerV)
	if err != nil {
		return nil, fmt.Errorf("assert_drag_follow finger: %w", err)
	}
	object, err := pointSeries(objectV)
	if err != nil {
		return nil, fmt.Errorf("assert_drag_follow object: %w", err)
	}
	if err := scriptlib.AssertDragFollow(finger, object, maxP95); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func builtinAssertSettle(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var awakeV starlark.Value
	var maxSteps int
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"awake", &awakeV,
		"max_steps", &maxSteps,
	); err != nil {
		return nil, err
	}
	list, ok := awakeV.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("assert_settle: awake must be a list")
	}
	awake := make([]bool, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		v := list.Index(i)
		bv, ok := v.(starlark.Bool)
		if !ok {
			return nil, fmt.Errorf("assert_settle: awake[%d] must be bool", i)
		}
		awake = append(awake, bool(bv))
	}
	if err := scriptlib.AssertSettle(awake, maxSteps); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func builtinResolveTarget(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var nodeV starlark.Value
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "node", &nodeV); err != nil {
		return nil, err
	}
	node, err := starlarkToGo(nodeV)
	if err != nil {
		return nil, err
	}
	m, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("resolve_target: node must be a dict")
	}
	cx, cy, err := scriptlib.ResolveTarget(m)
	if err != nil {
		return nil, err
	}
	d := starlark.NewDict(2)
	_ = d.SetKey(starlark.String("cx"), starlark.Float(cx))
	_ = d.SetKey(starlark.String("cy"), starlark.Float(cy))
	return d, nil
}

func builtinFindByLabel(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var nodesV starlark.Value
	var label string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "nodes", &nodesV, "label", &label); err != nil {
		return nil, err
	}
	raw, err := starlarkToGo(nodesV)
	if err != nil {
		return nil, err
	}
	nodes, err := scriptlib.NodesFromAny(raw)
	if err != nil {
		return nil, fmt.Errorf("find_by_label: %w", err)
	}
	node, err := scriptlib.FindByLabel(nodes, label)
	if err != nil {
		return nil, err
	}
	return goToStarlark(node), nil
}

func pointSeries(v starlark.Value) ([]scriptlib.Point, error) {
	list, ok := v.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("want list of {x,y}, got %s", v.Type())
	}
	out := make([]scriptlib.Point, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		el := list.Index(i)
		g, err := starlarkToGo(el)
		if err != nil {
			return nil, err
		}
		m, ok := g.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("element %d is not a dict", i)
		}
		x, okx := asFloatAny(m["x"])
		y, oky := asFloatAny(m["y"])
		if !okx || !oky {
			return nil, fmt.Errorf("element %d needs numeric x,y", i)
		}
		out = append(out, scriptlib.Point{X: x, Y: y})
	}
	return out, nil
}

func asFloatAny(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	default:
		return 0, false
	}
}

// handleListScripts lists durable library scripts (bundled + user).
func (h *Handler) handleListScripts(_ map[string]any) (*mcpgo.CallToolResult, error) {
	list, err := scriptlib.List()
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(map[string]any{"scripts": list, "user_dir": scriptlib.ScriptsDir()})
}

// handleRunScript loads a durable script and executes it via runExec.
func (h *Handler) handleRunScript(args map[string]any) (*mcpgo.CallToolResult, error) {
	ref, err := requireString(args, "path")
	if err != nil {
		// accept "name" alias
		ref, err = requireString(args, "name")
		if err != nil {
			return toolErr("run_script: path or name required")
		}
	}
	// Re-enter app_exec resolution path.
	execArgs := map[string]any{
		"script_path": ref,
	}
	if p, ok := args["params"]; ok {
		execArgs["params"] = p
	}
	if ms, ok := args["max_duration_ms"]; ok {
		execArgs["max_duration_ms"] = ms
	}
	return h.handleAppExec(execArgs)
}
