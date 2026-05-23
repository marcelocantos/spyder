// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"reflect"
	"sort"
	"testing"
)

func TestDiffUDIDs(t *testing.T) {
	cases := []struct {
		name        string
		usbmux      []string
		devicectl   []string
		wantMissing []string
		wantExtra   []string
	}{
		{
			name:      "both empty",
			usbmux:    nil,
			devicectl: nil,
		},
		{
			name:        "wedge - devicectl sees device usbmux doesn't",
			usbmux:      []string{"iPad-A"},
			devicectl:   []string{"iPad-A", "iPhone-B"},
			wantMissing: []string{"iPhone-B"},
		},
		{
			name:        "full wedge - both devices missing from usbmux",
			usbmux:      nil,
			devicectl:   []string{"iPad-A", "iPhone-B"},
			wantMissing: []string{"iPad-A", "iPhone-B"},
		},
		{
			name:      "consistent - both views agree",
			usbmux:    []string{"iPad-A", "iPhone-B"},
			devicectl: []string{"iPad-A", "iPhone-B"},
		},
		{
			name:      "stale usbmux - usbmux sees a device devicectl doesn't",
			usbmux:    []string{"iPad-A", "old-iPhone"},
			devicectl: []string{"iPad-A"},
			wantExtra: []string{"old-iPhone"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missing, extra := diffUDIDs(tc.usbmux, tc.devicectl)
			sort.Strings(missing)
			sort.Strings(extra)
			sort.Strings(tc.wantMissing)
			sort.Strings(tc.wantExtra)
			if !reflect.DeepEqual(missing, tc.wantMissing) {
				t.Errorf("missing = %v; want %v", missing, tc.wantMissing)
			}
			if !reflect.DeepEqual(extra, tc.wantExtra) {
				t.Errorf("extra = %v; want %v", extra, tc.wantExtra)
			}
		})
	}
}

func TestBuildSnapshot_FlagsWedge(t *testing.T) {
	s := buildSnapshot("iPhone-B", "test",
		[]string{"iPad-A"},
		[]string{"iPad-A", "iPhone-B"},
		"", "")
	if !s.Wedged {
		t.Error("expected Wedged=true when devicectl sees a device usbmux doesn't")
	}
	if len(s.MissingFromMux) != 1 || s.MissingFromMux[0] != "iPhone-B" {
		t.Errorf("MissingFromMux = %v; want [iPhone-B]", s.MissingFromMux)
	}
}

func TestBuildSnapshot_EmptyDevicectlNotAWedge(t *testing.T) {
	// devicectl returned nothing — no devices attached. usbmux is
	// trivially consistent (or also empty). Not a wedge.
	s := buildSnapshot("", "test", nil, nil, "", "")
	if s.Wedged {
		t.Error("empty inputs should not flag a wedge")
	}
}

func TestBuildSnapshot_ConsistentViewsNotAWedge(t *testing.T) {
	s := buildSnapshot("", "test",
		[]string{"iPad-A", "iPhone-B"},
		[]string{"iPad-A", "iPhone-B"},
		"", "")
	if s.Wedged {
		t.Error("consistent views should not flag a wedge")
	}
}
