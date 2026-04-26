// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"strings"
	"testing"
)

// ptr is a convenience helper for creating a pointer to any value.
func ptr[T any](v T) *T { return &v }

func TestParseSelectorString(t *testing.T) {
	type want struct {
		sel Selector
		err string // non-empty → expect an error containing this substring
	}

	cases := []struct {
		name  string
		input string
		want  want
	}{
		// --- Happy paths ---------------------------------------------------
		{
			name:  "platform only",
			input: "platform=ios",
			want:  want{sel: Selector{Platform: "ios"}},
		},
		{
			name:  "platform and model",
			input: "platform=ios,model=iPhone",
			want:  want{sel: Selector{Platform: "ios", ModelFamily: "iPhone"}},
		},
		{
			name:  "os>=",
			input: "platform=ios,os>=17",
			want:  want{sel: Selector{Platform: "ios", OSMin: "17"}},
		},
		{
			name:  "os<=",
			input: "platform=ios,os<=18.6",
			want:  want{sel: Selector{Platform: "ios", OSMax: "18.6"}},
		},
		{
			name:  "os>= and os<= together",
			input: "platform=ios,os>=17,os<=18.6",
			want:  want{sel: Selector{Platform: "ios", OSMin: "17", OSMax: "18.6"}},
		},
		{
			name:  "os_min alternate spelling",
			input: "os_min=17",
			want:  want{sel: Selector{OSMin: "17"}},
		},
		{
			name:  "os_max alternate spelling",
			input: "os_max=18.6",
			want:  want{sel: Selector{OSMax: "18.6"}},
		},
		{
			name:  "single tag",
			input: "tags=phone",
			want:  want{sel: Selector{Tags: []string{"phone"}}},
		},
		{
			name:  "multiple tags plus-separated",
			input: "tags=phone+test",
			want:  want{sel: Selector{Tags: []string{"phone", "test"}}},
		},
		{
			name:  "orientation_capable true",
			input: "orientation_capable=true",
			want:  want{sel: Selector{OrientationCapable: *ptr(true)}},
		},
		{
			name:  "orientation_capable false",
			input: "orientation_capable=false",
			want:  want{sel: Selector{OrientationCapable: *ptr(false)}},
		},
		{
			name:  "orientation_capable 1",
			input: "orientation_capable=1",
			want:  want{sel: Selector{OrientationCapable: true}},
		},
		{
			name:  "orientation_capable 0",
			input: "orientation_capable=0",
			want:  want{sel: Selector{OrientationCapable: false}},
		},
		{
			name:  "single attr",
			input: "attr.serial=ABC123",
			want:  want{sel: Selector{Attrs: map[string]string{"serial": "ABC123"}}},
		},
		{
			name:  "multiple attrs",
			input: "attr.serial=ABC,attr.region=us-east-1",
			want:  want{sel: Selector{Attrs: map[string]string{"serial": "ABC", "region": "us-east-1"}}},
		},
		{
			name:  "whitespace around keys and values",
			input: " platform = ios , model = iPhone ",
			want:  want{sel: Selector{Platform: "ios", ModelFamily: "iPhone"}},
		},
		{
			name:  "android platform",
			input: "platform=android",
			want:  want{sel: Selector{Platform: "android"}},
		},
		{
			name:  "ios-sim platform",
			input: "platform=ios-sim",
			want:  want{sel: Selector{Platform: "ios-sim"}},
		},

		// --- Error paths ---------------------------------------------------
		{
			name:  "empty string",
			input: "",
			want:  want{err: "empty selector string"},
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  want{err: "empty selector string"},
		},
		{
			name:  "single comma",
			input: ",",
			want:  want{err: "empty token"},
		},
		{
			name:  "leading comma",
			input: ",platform=ios",
			want:  want{err: "empty token"},
		},
		{
			name:  "trailing comma",
			input: "platform=ios,",
			want:  want{err: "empty token"},
		},
		{
			name:  "unknown key",
			input: "foo=bar",
			want:  want{err: `unknown key "foo"`},
		},
		{
			name:  "os= is ambiguous",
			input: "os=17",
			want:  want{err: "ambiguous key"},
		},
		{
			name:  "duplicate platform",
			input: "platform=ios,platform=android",
			want:  want{err: `duplicate key "platform"`},
		},
		{
			name:  "duplicate os_min",
			input: "os_min=17,os_min=18",
			want:  want{err: `duplicate key "os_min"`},
		},
		{
			name:  "invalid bool",
			input: "orientation_capable=maybe",
			want:  want{err: "invalid bool value"},
		},
		{
			name:  "empty attr name",
			input: "attr.=value",
			want:  want{err: "empty attr name"},
		},
		{
			name:  "bare equals",
			input: "=",
			want:  want{err: "empty key"},
		},
		{
			name:  "empty value",
			input: "platform=",
			want:  want{err: "empty value"},
		},
		{
			name:  "empty key with value",
			input: "=ios",
			want:  want{err: "empty key"},
		},
		{
			name:  "token without equals",
			input: "platform",
			want:  want{err: "missing '='"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSelectorString(tc.input)

			if tc.want.err != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (result: %+v)", tc.want.err, got)
				}
				if !strings.Contains(err.Error(), tc.want.err) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.want.err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			want := tc.want.sel
			if got.Platform != want.Platform {
				t.Errorf("Platform: got %q, want %q", got.Platform, want.Platform)
			}
			if got.ModelFamily != want.ModelFamily {
				t.Errorf("ModelFamily: got %q, want %q", got.ModelFamily, want.ModelFamily)
			}
			if got.OSMin != want.OSMin {
				t.Errorf("OSMin: got %q, want %q", got.OSMin, want.OSMin)
			}
			if got.OSMax != want.OSMax {
				t.Errorf("OSMax: got %q, want %q", got.OSMax, want.OSMax)
			}
			if got.OrientationCapable != want.OrientationCapable {
				t.Errorf("OrientationCapable: got %v, want %v", got.OrientationCapable, want.OrientationCapable)
			}
			if !stringSlicesEqual(got.Tags, want.Tags) {
				t.Errorf("Tags: got %v, want %v", got.Tags, want.Tags)
			}
			if !stringMapsEqual(got.Attrs, want.Attrs) {
				t.Errorf("Attrs: got %v, want %v", got.Attrs, want.Attrs)
			}
		})
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
