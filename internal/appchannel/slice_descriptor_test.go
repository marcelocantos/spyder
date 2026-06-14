// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"bytes"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// TestSliceDescriptor_DecodeBareString verifies pre-T81 apps that
// send `slices: ["scene"]` still decode cleanly into the new struct
// shape. The Name is populated; Example stays nil.
func TestSliceDescriptor_DecodeBareString(t *testing.T) {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	if err := enc.Encode([]string{"scene", "physics", "hud"}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out []SliceDescriptor
	if err := msgpack.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d; want 3", len(out))
	}
	for i, want := range []string{"scene", "physics", "hud"} {
		if out[i].Name != want {
			t.Errorf("Slices[%d].Name = %q; want %q", i, out[i].Name, want)
		}
		if out[i].Example != nil {
			t.Errorf("Slices[%d].Example = %v; want nil", i, out[i].Example)
		}
	}
}

// TestSliceDescriptor_DecodeStructForm verifies T81+ apps that send
// `slices: [{name: "scene", example: {...}}]` decode into the struct
// with both fields populated.
func TestSliceDescriptor_DecodeStructForm(t *testing.T) {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	in := []map[string]any{
		{"name": "physics", "example": map[string]any{
			"marble": map[string]any{
				"position": map[string]any{"x": 0.0, "y": 0.0, "z": 0.0},
			},
		}},
	}
	if err := enc.Encode(in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out []SliceDescriptor
	if err := msgpack.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Name != "physics" {
		t.Fatalf("descriptors = %v", out)
	}
	if out[0].Example == nil {
		t.Fatal("Example nil; expected populated map")
	}
	// Drill in to make sure the nested example survived.
	m, ok := out[0].Example.(map[string]any)
	if !ok {
		t.Fatalf("example type = %T; want map[string]any", out[0].Example)
	}
	if _, ok := m["marble"]; !ok {
		t.Errorf("example missing marble field: %v", m)
	}
}

// TestSliceDescriptor_DecodeMixed verifies a list mixing bare strings
// and struct entries decodes cleanly — apps may upgrade slice-by-slice
// during a roll-out.
func TestSliceDescriptor_DecodeMixed(t *testing.T) {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	// Heterogeneous slice — encode as []any so each element can have
	// a different msgpack type (string vs map).
	in := []any{
		"scene",
		map[string]any{"name": "physics", "example": map[string]any{"k": 1}},
		"hud",
	}
	if err := enc.Encode(in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out []SliceDescriptor
	if err := msgpack.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d; want 3", len(out))
	}
	if out[0].Name != "scene" || out[0].Example != nil {
		t.Errorf("Slices[0] = %+v; want {Name: scene, Example: nil}", out[0])
	}
	if out[1].Name != "physics" || out[1].Example == nil {
		t.Errorf("Slices[1] = %+v; want struct form with example", out[1])
	}
	if out[2].Name != "hud" || out[2].Example != nil {
		t.Errorf("Slices[2] = %+v; want {Name: hud, Example: nil}", out[2])
	}
}
