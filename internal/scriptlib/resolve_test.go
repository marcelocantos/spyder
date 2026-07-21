// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package scriptlib

import "testing"

func TestResolveTargetScreen(t *testing.T) {
	cx, cy, err := ResolveTarget(map[string]any{
		"screen": map[string]any{"cx": 0.4, "cy": 0.6},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cx != 0.4 || cy != 0.6 {
		t.Fatalf("got %v,%v", cx, cy)
	}
}

func TestResolveTargetBBox(t *testing.T) {
	cx, cy, err := ResolveTarget(map[string]any{
		"bbox": []any{10.0, 20.0, 40.0, 60.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cx != 30 || cy != 50 {
		t.Fatalf("got %v,%v", cx, cy)
	}
}

func TestResolveTargetMissing(t *testing.T) {
	_, _, err := ResolveTarget(map[string]any{"label": "buggy"})
	if err == nil {
		t.Fatal("expected slice contract error")
	}
}

func TestResolveTargetCenterNorm(t *testing.T) {
	cx, cy, err := ResolveTarget(map[string]any{
		"id":          "reset",
		"center_norm": []any{0.5, 0.07},
		// pts bbox must not win over center_norm for inject
		"bbox": []any{100.0, 8.0, 300.0, 67.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cx != 0.5 || cy != 0.07 {
		t.Fatalf("got %v,%v want center_norm", cx, cy)
	}
}

func TestResolveTargetBBoxNorm(t *testing.T) {
	cx, cy, err := ResolveTarget(map[string]any{
		"bbox_norm": []any{0.2, 0.0, 0.6, 0.1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cx != 0.5 || cy != 0.05 {
		t.Fatalf("got %v,%v", cx, cy)
	}
}

func TestFindByLabel(t *testing.T) {
	nodes := []map[string]any{
		{"label": "a", "screen": map[string]any{"cx": 1.0, "cy": 2.0}},
		{"name": "buggy", "bbox": []any{0.0, 0.0, 10.0, 10.0}},
	}
	n, err := FindByLabel(nodes, "buggy")
	if err != nil {
		t.Fatal(err)
	}
	cx, cy, err := ResolveTarget(n)
	if err != nil {
		t.Fatal(err)
	}
	if cx != 5 || cy != 5 {
		t.Fatalf("got %v,%v", cx, cy)
	}
	if _, err := FindByLabel(nodes, "missing"); err == nil {
		t.Fatal("expected missing label error")
	}
}

func TestFindHitTargetPrefersIDOverLabel(t *testing.T) {
	nodes := []map[string]any{
		{"id": "other", "label": "reset", "center_norm": []any{0.1, 0.1}},
		{"id": "reset", "role": "reset", "label": "TiltBuggy", "center_norm": []any{0.5, 0.05}},
	}
	n, err := FindHitTarget(nodes, "reset")
	if err != nil {
		t.Fatal(err)
	}
	if n["id"] != "reset" {
		t.Fatalf("expected id=reset, got %+v", n)
	}
	// Label-only match must not satisfy FindHitTarget.
	if _, err := FindHitTarget(nodes, "TiltBuggy"); err == nil {
		t.Fatal("label must not address hit targets")
	}
}

func TestFindHitTargetByRole(t *testing.T) {
	nodes := []map[string]any{
		{"id": "btn1", "role": "reset", "center_norm": []any{0.5, 0.05}},
	}
	n, err := FindHitTarget(nodes, "reset")
	if err != nil {
		t.Fatal(err)
	}
	if n["id"] != "btn1" {
		t.Fatalf("%+v", n)
	}
}

func TestFindHitTargetDisabled(t *testing.T) {
	nodes := []map[string]any{
		{"id": "reset", "enabled": false, "center_norm": []any{0.5, 0.05}},
	}
	if _, err := FindHitTarget(nodes, "reset"); err == nil {
		t.Fatal("expected disabled error")
	}
}

func TestFindHitTargetMissing(t *testing.T) {
	if _, err := FindHitTarget(nil, "reset"); err == nil {
		t.Fatal("expected missing")
	}
}

func TestTargetsFromHitSlice(t *testing.T) {
	nodes, err := TargetsFromHitSlice(map[string]any{
		"targets": []any{
			map[string]any{"id": "reset", "center_norm": []any{0.5, 0.05}},
			map[string]any{"id": "playfield", "kind": "region", "bbox_norm": []any{0, 0, 1, 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("len=%d", len(nodes))
	}
	n, err := FindHitTarget(nodes, "reset")
	if err != nil {
		t.Fatal(err)
	}
	cx, cy, err := ResolveTarget(n)
	if err != nil {
		t.Fatal(err)
	}
	if cx != 0.5 || cy != 0.05 {
		t.Fatalf("%v,%v", cx, cy)
	}
}

func TestNodesFromBodiesMap(t *testing.T) {
	nodes, err := NodesFromAny(map[string]any{
		"buggy": map[string]any{"screen": map[string]any{"cx": 0.5, "cy": 0.5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("len=%d", len(nodes))
	}
	if nodes[0]["label"] != "buggy" {
		t.Fatalf("%+v", nodes[0])
	}
}
