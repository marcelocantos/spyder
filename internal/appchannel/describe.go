// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	// describeMaxDepth caps recursion when walking a slice response.
	// Deeper nodes collapse to "<truncated>" so a runaway-recursive
	// payload doesn't explode the sketch.
	describeMaxDepth = 8

	// describeMaxArrayElements caps how many array elements
	// contribute to type inference. After this many, the walker
	// stops looking — the inferred type is whatever was seen so far.
	describeMaxArrayElements = 4
)

// DescribeShape walks a MessagePack-decoded value and emits a recursive
// types-only sketch suitable for an agent to infer jq expressions
// against. Maps become `{key: <type>, ...}`; arrays become a one-
// element `[<type>]` with the type inferred from up to
// describeMaxArrayElements entries; primitives become their type
// names ("string", "int", "float", "bool", "null"). Depth is bounded.
//
// This is what `app_state_describe` returns — a single
// `state_query{slice}` call's response, walked into a structure-only
// form. The agent sees field names and types but not values, so the
// cost is bounded regardless of how large the slice is.
func DescribeShape(raw msgpack.RawMessage) (any, error) {
	var v any
	if err := msgpack.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return describe(v, 0), nil
}

func describe(v any, depth int) any {
	if depth >= describeMaxDepth {
		return "<truncated>"
	}
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return "int"
	case float32, float64:
		return "float"
	case []any:
		if len(x) == 0 {
			return []any{}
		}
		// Walk up to describeMaxArrayElements, unify the shapes. If
		// they're all consistent, emit `[<shape>]`; if not, emit the
		// distinct shapes as a single representative followed by a
		// "...mixed" note.
		limit := len(x)
		if limit > describeMaxArrayElements {
			limit = describeMaxArrayElements
		}
		first := describe(x[0], depth+1)
		consistent := true
		for i := 1; i < limit; i++ {
			next := describe(x[i], depth+1)
			if !shapesEqual(first, next) {
				consistent = false
				break
			}
		}
		if consistent {
			return []any{first}
		}
		return []any{first, "...mixed"}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = describe(val, depth+1)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprintf("%v", k)
			}
			out[ks] = describe(val, depth+1)
		}
		return out
	default:
		return fmt.Sprintf("%T", v)
	}
}

// shapesEqual is a structural-equality helper used to decide whether
// every element of an array has the same describe()-emitted shape.
// Maps compare by key set + per-key shape; arrays by inner shape;
// primitives by string equality.
func shapesEqual(a, b any) bool {
	switch ax := a.(type) {
	case string:
		bs, ok := b.(string)
		return ok && ax == bs
	case []any:
		bx, ok := b.([]any)
		if !ok || len(ax) != len(bx) {
			return false
		}
		for i := range ax {
			if !shapesEqual(ax[i], bx[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bx, ok := b.(map[string]any)
		if !ok || len(ax) != len(bx) {
			return false
		}
		for k, v := range ax {
			bv, ok := bx[k]
			if !ok || !shapesEqual(v, bv) {
				return false
			}
		}
		return true
	}
	return false
}
