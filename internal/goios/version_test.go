// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import "testing"

func TestParseIOSMajor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"17.4.1", 17},
		{"17.0", 17},
		{"17", 17},
		{"16.7.10", 16},
		{"16.7", 16},
		{"16", 16},
		{"9.3.5", 9},
		{"", 0},
		{"bogus", 0},
		{"not.a.version", 0},
		{".5", 0},
		{"18.0-beta1", 18},
	}
	for _, c := range cases {
		got := ParseIOSMajor(c.in)
		if got != c.want {
			t.Errorf("ParseIOSMajor(%q) = %d; want %d", c.in, got, c.want)
		}
	}
}
