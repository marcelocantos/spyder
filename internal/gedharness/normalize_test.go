// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"encoding/json"
	"reflect"
	"testing"
)

// decodeAny unmarshals JSON text into an `any` tree for normalize tests.
func decodeAny(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		rules []Rule
		in    string
		want  string
	}{
		{
			name:  "zero replaces scalar with typed zero",
			rules: []Rule{{Path: "pid", Action: Zero}},
			in:    `{"pid":4242,"name":"srv"}`,
			want:  `{"pid":0,"name":"srv"}`,
		},
		{
			name:  "zero string field",
			rules: []Rule{{Path: "id", Action: Zero}},
			in:    `{"id":"abc123"}`,
			want:  `{"id":""}`,
		},
		{
			name:  "drop removes the key entirely",
			rules: []Rule{{Path: "timestamp", Action: Drop}},
			in:    `{"timestamp":"2026-01-01","msg":"hi"}`,
			want:  `{"msg":"hi"}`,
		},
		{
			name:  "wildcard array index zeroes every element field",
			rules: []Rule{{Path: "servers/*/pid", Action: Zero}},
			in:    `{"servers":[{"id":"a","pid":11},{"id":"b","pid":22}]}`,
			want:  `{"servers":[{"id":"a","pid":0},{"id":"b","pid":0}]}`,
		},
		{
			name:  "wildcard object key drops matching child under any parent",
			rules: []Rule{{Path: "*/ts", Action: Drop}},
			in:    `{"a":{"ts":1,"v":2},"b":{"ts":3,"v":4}}`,
			want:  `{"a":{"v":2},"b":{"v":4}}`,
		},
		{
			name:  "array of objects sorted by id",
			rules: nil,
			in:    `[{"id":"z"},{"id":"a"},{"id":"m"}]`,
			want:  `[{"id":"a"},{"id":"m"},{"id":"z"}]`,
		},
		{
			name:  "array of objects sorted by name when no id",
			rules: nil,
			in:    `[{"name":"beta"},{"name":"alpha"}]`,
			want:  `[{"name":"alpha"},{"name":"beta"}]`,
		},
		{
			name:  "scalar array is left in source order",
			rules: nil,
			in:    `[3,1,2]`,
			want:  `[3,1,2]`,
		},
		{
			name:  "no matching rule leaves value unchanged",
			rules: []Rule{{Path: "nope", Action: Zero}},
			in:    `{"keep":123}`,
			want:  `{"keep":123}`,
		},
		{
			name:  "zero on nested array count field",
			rules: []Rule{{Path: "servers/*/sessions", Action: Zero}},
			in:    `{"servers":[{"id":"a","sessions":7}]}`,
			want:  `{"servers":[{"id":"a","sessions":0}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(decodeAny(t, tt.in), tt.rules)
			want := decodeAny(t, tt.want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Normalize()\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

// TestNormalizeIdempotentAcrossRuns is the core property: two ged
// responses that differ only in PID and timestamp normalize to identical
// trees, so a downstream diff sees nothing.
func TestNormalizeIdempotentAcrossRuns(t *testing.T) {
	runA := `{"connected":true,"servers":[{"id":"s7","name":"game","pid":1111,"sessions":2}],"sessions":2,"timestamp":"2026-07-04T00:00:00Z"}`
	runB := `{"connected":true,"servers":[{"id":"s9","name":"game","pid":2222,"sessions":5}],"sessions":5,"timestamp":"2026-07-04T12:34:56Z"}`

	rules := DefaultGedRules()
	na := Normalize(decodeAny(t, runA), rules)
	nb := Normalize(decodeAny(t, runB), rules)

	if !reflect.DeepEqual(na, nb) {
		t.Errorf("PID/session/timestamp differences did not normalize away:\n A: %#v\n B: %#v", na, nb)
	}
}

// TestNormalizeStableUnderReordering: two responses that list the same
// servers in different order normalize identically (object-array sort).
func TestNormalizeStableUnderReordering(t *testing.T) {
	a := `{"servers":[{"id":"a","name":"x"},{"id":"b","name":"y"}]}`
	b := `{"servers":[{"id":"b","name":"y"},{"id":"a","name":"x"}]}`
	rules := DefaultGedRules()
	if !reflect.DeepEqual(Normalize(decodeAny(t, a), rules), Normalize(decodeAny(t, b), rules)) {
		t.Error("server ordering difference did not normalize away")
	}
}

// TestNormalizeDoesNotMutateInput proves purity: the caller's tree is
// untouched.
func TestNormalizeDoesNotMutateInput(t *testing.T) {
	in := decodeAny(t, `{"servers":[{"id":"a","pid":99}]}`)
	before, _ := json.Marshal(in)
	_ = Normalize(in, DefaultGedRules())
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Errorf("input mutated:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestNormalizeDoesNotZeroMissingField: Zero keeps a present field but
// must not fabricate an absent one. A response lacking `sessions` stays
// without it, so the differ can still flag its absence.
func TestNormalizeDoesNotZeroMissingField(t *testing.T) {
	got := Normalize(decodeAny(t, `{"connected":false}`), DefaultGedRules())
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("want object, got %T", got)
	}
	if _, present := m["sessions"]; present {
		t.Error("Zero fabricated an absent field")
	}
}
