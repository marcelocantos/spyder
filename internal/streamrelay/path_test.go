// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package streamrelay

import "testing"

func TestClassifyRemote(t *testing.T) {
	cases := []struct {
		in   string
		want PathClass
	}{
		{"127.0.0.1:3030", PathLoopback},
		{"127.0.0.1", PathLoopback},
		{"[::1]:3030", PathLoopback},
		{"::1", PathLoopback},
		{"192.168.1.193:57501", PathLAN},
		{"10.0.0.5:9", PathLAN},
		{"172.16.4.1:1", PathLAN},
		{"169.254.1.1:1", PathLAN},
		{"8.8.8.8:443", PathPublic},
		{"1.1.1.1", PathPublic},
		{"[2001:4860:4860::8888]:443", PathPublic},
		{"not-an-ip:3030", PathUnknown},
		{"", PathUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyRemote(tc.in); got != tc.want {
			t.Errorf("ClassifyRemote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
