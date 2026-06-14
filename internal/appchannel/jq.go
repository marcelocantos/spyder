// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"time"

	"github.com/itchyny/gojq"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	// jqEvalTimeout bounds a single ApplyJQ evaluation. jq programs
	// can recurse / iterate arbitrarily; this keeps a hostile (or
	// merely silly) expression from monopolising a handler goroutine.
	jqEvalTimeout = 2 * time.Second
)

// JQError is returned from ApplyJQ when an expression fails to parse
// or evaluate. Distinct type so handlers can render it as a structured
// `select_error` field rather than an opaque string.
type JQError struct {
	Expression string `json:"expression"`
	Stage      string `json:"stage"` // "parse", "compile", "eval"
	Detail     string `json:"detail"`
}

func (e *JQError) Error() string {
	return fmt.Sprintf("jq %s error in %q: %s", e.Stage, e.Expression, e.Detail)
}

// ApplyJQ evaluates expr over the value decoded from raw (MessagePack)
// and returns the result as a generic Go value suitable for JSON
// emission. Returns an empty slice if expr evaluates to nothing —
// that's jq's natural "no matches" semantics, not an error.
//
// Multi-result expressions (e.g. `.entities[]`) return a []any with
// one element per match. Single-result expressions return that value
// directly to avoid wrapping cost on the common case.
func ApplyJQ(expr string, raw msgpack.RawMessage) (any, error) {
	if expr == "" {
		// No filter: decode raw and return verbatim.
		var v any
		if err := msgpack.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return v, nil
	}

	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, &JQError{Expression: expr, Stage: "parse", Detail: err.Error()}
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, &JQError{Expression: expr, Stage: "compile", Detail: err.Error()}
	}

	var input any
	if err := msgpack.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	input = normaliseForJQ(input)

	ctx, cancel := context.WithTimeout(context.Background(), jqEvalTimeout)
	defer cancel()

	// gojq occasionally panics during error formatting on certain
	// type-mismatch cases (e.g. `.a.b` on `{a: 1}` triggers a panic in
	// expectedObjectError.Error). Recover so the caller sees a clean
	// JQError instead of a daemon crash.
	var results []any
	var recovered *JQError
	func() {
		defer func() {
			if r := recover(); r != nil {
				recovered = &JQError{Expression: expr, Stage: "eval", Detail: fmt.Sprintf("%v", r)}
			}
		}()
		iter := code.RunWithContext(ctx, input)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, isErr := v.(error); isErr {
				recovered = &JQError{Expression: expr, Stage: "eval", Detail: err.Error()}
				return
			}
			results = append(results, v)
		}
	}()
	if recovered != nil {
		return nil, recovered
	}

	switch len(results) {
	case 0:
		return []any{}, nil
	case 1:
		return results[0], nil
	default:
		return results, nil
	}
}

// normaliseForJQ converts msgpack-decoded values into shapes gojq can
// consume. msgpack/v5's generic decoder produces map[string]any for
// most maps already (good), but arrays come through as []any (also
// good). The work this function does:
//
//   - map[any]any → map[string]any (gojq requires string-keyed maps;
//     msgpack maps with non-string keys are unusual but not impossible)
//   - Numeric types (int8/16/32/64, uint*, float32) → float64 or int64
//     (gojq operates on float64/int64; mixing types confuses comparisons)
//   - Recursive descent through slices and maps so nested values are
//     also normalised
func normaliseForJQ(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = normaliseForJQ(val)
		}
		return x
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprintf("%v", k)
			}
			out[ks] = normaliseForJQ(val)
		}
		return out
	case []any:
		for i, val := range x {
			x[i] = normaliseForJQ(val)
		}
		return x
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case uint:
		return int64(x)
	case float32:
		return float64(x)
	default:
		return v
	}
}
