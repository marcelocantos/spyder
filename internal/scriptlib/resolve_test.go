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
