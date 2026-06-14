// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"errors"
	"testing"
)

func TestApplyJQ_HappyPath(t *testing.T) {
	raw, _ := PackParams(map[string]any{
		"marble": map[string]any{
			"position": map[string]any{"x": 1.5, "y": 2.5, "z": 0.0},
			"velocity": map[string]any{"x": 0.0, "y": 0.0, "z": 0.0},
		},
	})
	out, err := ApplyJQ(".marble.position.x", raw)
	if err != nil {
		t.Fatalf("ApplyJQ: %v", err)
	}
	if out != 1.5 {
		t.Errorf("ApplyJQ = %v; want 1.5", out)
	}
}

func TestApplyJQ_MultiResult(t *testing.T) {
	raw, _ := PackParams(map[string]any{
		"entities": []any{
			map[string]any{"id": 1},
			map[string]any{"id": 2},
			map[string]any{"id": 3},
		},
	})
	out, err := ApplyJQ(".entities[].id", raw)
	if err != nil {
		t.Fatalf("ApplyJQ: %v", err)
	}
	results, ok := out.([]any)
	if !ok {
		t.Fatalf("expected []any; got %T (%v)", out, out)
	}
	if len(results) != 3 {
		t.Errorf("len = %d; want 3", len(results))
	}
}

func TestApplyJQ_EmptyExpressionPassesThrough(t *testing.T) {
	raw, _ := PackParams(map[string]any{"foo": 42})
	out, err := ApplyJQ("", raw)
	if err != nil {
		t.Fatalf("ApplyJQ: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok || m["foo"] == nil {
		t.Errorf("expected map with foo=42; got %v", out)
	}
}

func TestApplyJQ_ParseError(t *testing.T) {
	raw, _ := PackParams(map[string]any{"a": 1})
	_, err := ApplyJQ(".[this is not valid jq", raw)
	if err == nil {
		t.Fatal("expected parse error")
	}
	var jqErr *JQError
	if !errors.As(err, &jqErr) {
		t.Fatalf("expected *JQError; got %T", err)
	}
	if jqErr.Stage != "parse" {
		t.Errorf("Stage = %q; want parse", jqErr.Stage)
	}
}

func TestApplyJQ_NoMatchesReturnsEmpty(t *testing.T) {
	raw, _ := PackParams(map[string]any{"entities": []any{}})
	out, err := ApplyJQ(".entities[]", raw)
	if err != nil {
		t.Fatalf("ApplyJQ: %v", err)
	}
	results, ok := out.([]any)
	if !ok || len(results) != 0 {
		t.Errorf("expected empty []any; got %T (%v)", out, out)
	}
}

func TestApplyJQ_EvalError(t *testing.T) {
	raw, _ := PackParams(map[string]any{"a": 1})
	// `.a.b` errors because 1 is not a map — jq raises a runtime error.
	_, err := ApplyJQ(".a.b", raw)
	if err == nil {
		t.Fatal("expected eval error for `.a.b` on `{a: 1}`")
	}
	var jqErr *JQError
	if !errors.As(err, &jqErr) {
		t.Fatalf("expected *JQError; got %T", err)
	}
	if jqErr.Stage != "eval" {
		t.Errorf("Stage = %q; want eval", jqErr.Stage)
	}
}

func TestApplyJQ_NumericNormalisation(t *testing.T) {
	// msgpack-encoded ints come back as int64 or uint64 depending on
	// sign/size. jq comparisons should still work.
	raw, _ := PackParams(map[string]any{
		"i8":  int8(42),
		"i32": int32(-100),
		"u32": uint32(7),
	})
	out, err := ApplyJQ("[.i8, .i32, .u32]", raw)
	if err != nil {
		t.Fatalf("ApplyJQ: %v", err)
	}
	arr, ok := out.([]any)
	if !ok || len(arr) != 3 {
		t.Fatalf("expected 3-element array; got %T (%v)", out, out)
	}
}
