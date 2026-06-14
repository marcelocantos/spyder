// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"testing"
)

func TestDescribeShape_NestedMap(t *testing.T) {
	raw, _ := PackParams(map[string]any{
		"marble": map[string]any{
			"position": map[string]any{"x": 1.5, "y": 2.5, "z": 0.0},
			"id":       42,
			"active":   true,
			"name":     "player",
		},
	})
	shape, err := DescribeShape(raw)
	if err != nil {
		t.Fatalf("DescribeShape: %v", err)
	}
	root, ok := shape.(map[string]any)
	if !ok {
		t.Fatalf("expected map; got %T", shape)
	}
	marble, ok := root["marble"].(map[string]any)
	if !ok {
		t.Fatalf("marble shape: %T", root["marble"])
	}
	if marble["id"] != "int" {
		t.Errorf("id type = %v; want int", marble["id"])
	}
	if marble["active"] != "bool" {
		t.Errorf("active type = %v; want bool", marble["active"])
	}
	if marble["name"] != "string" {
		t.Errorf("name type = %v; want string", marble["name"])
	}
	pos, ok := marble["position"].(map[string]any)
	if !ok {
		t.Fatalf("position shape: %T", marble["position"])
	}
	if pos["x"] != "float" {
		t.Errorf("x type = %v; want float", pos["x"])
	}
}

func TestDescribeShape_HomogeneousArray(t *testing.T) {
	raw, _ := PackParams(map[string]any{
		"entities": []any{
			map[string]any{"id": 1, "name": "a"},
			map[string]any{"id": 2, "name": "b"},
			map[string]any{"id": 3, "name": "c"},
		},
	})
	shape, _ := DescribeShape(raw)
	root := shape.(map[string]any)
	entities, ok := root["entities"].([]any)
	if !ok || len(entities) != 1 {
		t.Fatalf("expected single-element array shape; got %T %v", root["entities"], root["entities"])
	}
	elem, ok := entities[0].(map[string]any)
	if !ok {
		t.Fatalf("element shape: %T", entities[0])
	}
	if elem["id"] != "int" {
		t.Errorf("element id type = %v; want int", elem["id"])
	}
}

func TestDescribeShape_MixedTypeArray(t *testing.T) {
	raw, _ := PackParams(map[string]any{
		"mixed": []any{1, "two", true, 3.14},
	})
	shape, _ := DescribeShape(raw)
	root := shape.(map[string]any)
	mixed, ok := root["mixed"].([]any)
	if !ok || len(mixed) != 2 {
		t.Fatalf("mixed shape = %v; want length 2 (sample + ...mixed marker)", root["mixed"])
	}
	if mixed[1] != "...mixed" {
		t.Errorf("expected ...mixed marker; got %v", mixed[1])
	}
}

func TestDescribeShape_EmptyArray(t *testing.T) {
	raw, _ := PackParams(map[string]any{"empty": []any{}})
	shape, _ := DescribeShape(raw)
	root := shape.(map[string]any)
	if arr, ok := root["empty"].([]any); !ok || len(arr) != 0 {
		t.Errorf("empty array shape = %v; want []", root["empty"])
	}
}

func TestDescribeShape_DepthCap(t *testing.T) {
	// Build a deeply nested map ten levels deep — describeMaxDepth is
	// 8, so level 8 should collapse to "<truncated>".
	deep := map[string]any{"leaf": 1}
	for i := 0; i < 12; i++ {
		deep = map[string]any{"down": deep}
	}
	raw, _ := PackParams(deep)
	shape, _ := DescribeShape(raw)

	cur := shape
	for i := 0; i < describeMaxDepth-1; i++ {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("level %d: expected map; got %T", i, cur)
		}
		cur = m["down"]
	}
	// At describeMaxDepth, the value should be "<truncated>".
	m, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("expected map at depth limit; got %T", cur)
	}
	if m["down"] != "<truncated>" {
		t.Errorf("depth-cap value = %v; want <truncated>", m["down"])
	}
}

func TestDescribeShape_NullValue(t *testing.T) {
	raw, _ := PackParams(map[string]any{"absent": nil, "present": 1})
	shape, _ := DescribeShape(raw)
	root := shape.(map[string]any)
	if root["absent"] != "null" {
		t.Errorf("absent shape = %v; want null", root["absent"])
	}
	if root["present"] != "int" {
		t.Errorf("present shape = %v; want int", root["present"])
	}
}
