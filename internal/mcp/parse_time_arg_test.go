// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestParseTimeArg(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name      string
		in        string
		wantErr   bool
		wantNear  time.Time // for relative forms
		tolerance time.Duration
		wantExact time.Time // for absolute forms
	}{
		{
			name:      "now keyword",
			in:        "now",
			wantNear:  now,
			tolerance: 100 * time.Millisecond,
		},
		{
			name:      "negative duration",
			in:        "-2m",
			wantNear:  now.Add(-2 * time.Minute),
			tolerance: 100 * time.Millisecond,
		},
		{
			name:      "positive duration",
			in:        "+30s",
			wantNear:  now.Add(30 * time.Second),
			tolerance: 100 * time.Millisecond,
		},
		{
			name:      "compound duration",
			in:        "-1h30m",
			wantNear:  now.Add(-90 * time.Minute),
			tolerance: 100 * time.Millisecond,
		},
		{
			name:      "bare zero",
			in:        "0",
			wantNear:  now,
			tolerance: 100 * time.Millisecond,
		},
		{
			name:      "whitespace trimmed",
			in:        "  -2m  ",
			wantNear:  now.Add(-2 * time.Minute),
			tolerance: 100 * time.Millisecond,
		},
		{
			name:      "RFC3339 absolute",
			in:        "2026-05-17T16:43:24Z",
			wantExact: time.Date(2026, 5, 17, 16, 43, 24, 0, time.UTC),
		},
		{
			name:    "garbage",
			in:      "not-a-timestamp",
			wantErr: true,
		},
		{
			name:    "RFC3339-like but invalid month",
			in:      "2026-13-01T00:00:00Z",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTimeArg(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got %v", tc.in, got)
				}
				// Error message must name both accepted forms so callers know
				// what to try next.
				if !strings.Contains(err.Error(), "Go duration") ||
					!strings.Contains(err.Error(), "RFC3339") {
					t.Errorf("error should mention both forms; got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantExact.IsZero() {
				if !got.Equal(tc.wantExact) {
					t.Errorf("got %v, want %v", got, tc.wantExact)
				}
				return
			}
			delta := got.Sub(tc.wantNear)
			if delta < 0 {
				delta = -delta
			}
			if delta > tc.tolerance {
				t.Errorf("got %v, want near %v (delta %v > tolerance %v)",
					got, tc.wantNear, delta, tc.tolerance)
			}
		})
	}
}
