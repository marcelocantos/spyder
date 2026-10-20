// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"
)

func TestBuildRecordIdentityScalarShapes(t *testing.T) {
	cases := []struct {
		name  string
		value stackItem
		want  uint32
	}{
		{"uint32_max", []byte{0xff, 0xff, 0xff, 0xff}, math.MaxUint32},
		{"tuple_first_scalar_only", []stackItem{[]byte{42}, []byte{99}}, 42},
		{"nil", nil, 0},
		{"empty_bytes", []byte{}, 0},
		{"empty_tuple", []stackItem{}, 0},
		{"missing_first_no_fallback", []stackItem{nil, []byte{42}}, 0},
		{"empty_first_no_fallback", []stackItem{[]byte{}, []byte{42}}, 0},
		{"string_first_no_fallback", []stackItem{"42", []byte{42}}, 0},
		{"nested_first_no_flattening", []stackItem{[]stackItem{[]byte{42}}, []byte{42}}, 0},
		{"string", "42", 0},
		{"integer", 42, 0},
		{"uint32_overflow", []byte{0, 0, 0, 0, 1}, 0},
		{"overflow_in_ninth_byte", []byte{42, 0, 0, 0, 0, 0, 0, 0, 1}, 0},
		{"over_wire_max_even_zero_padded", []byte{42, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 0},
		{"tuple_overflow", []stackItem{[]byte{0, 0, 0, 0, 1}, []byte{42}}, 0},
	}
	// The acceptance range is literal, independent of the implementation's
	// bound: legacy one-byte inputs and source-documented padding up to nine.
	for _, width := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9} {
		b := make([]byte, width)
		b[0] = 42
		cases = append(cases, struct {
			name  string
			value stackItem
			want  uint32
		}{"legacy_width_" + strconv.Itoa(width), b, 42})
		cases = append(cases, struct {
			name  string
			value stackItem
			want  uint32
		}{"tuple_width_" + strconv.Itoa(width), []stackItem{b, []byte{99}}, 42})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := []stackItem{tc.value, tc.value, []byte("/test/StockCars\x00"), []stackItem{[]stackItem{[]byte("narrative-text"), []byte("fixture")}}, []byte{7}, []byte("category\x00")}
			before, err := json.Marshal(items)
			if err != nil {
				t.Fatal(err)
			}
			rec, ok := buildRecord([]string{"process", "thread", "process-image-path", "message", "time", "category"}, items)
			if !ok || rec.PID != tc.want || rec.ThreadID != tc.want {
				t.Fatalf("identity=(%d,%d), want %d", rec.PID, rec.ThreadID, tc.want)
			}
			if rec.ImageName != "StockCars" || rec.Message != "fixture" || rec.Timestamp != 7 || rec.Category != "category" {
				t.Fatalf("unrelated fields changed: %+v", rec)
			}
			if !reflect.DeepEqual(rec.Columns["process"], tc.value) || !reflect.DeepEqual(rec.Columns["thread"], tc.value) {
				t.Fatal("raw identity Columns changed")
			}
			after, err := json.Marshal(items)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("record normalization mutated source items")
			}
		})
	}
}
