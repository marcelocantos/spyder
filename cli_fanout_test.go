// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

// TestLooksLikeFanOutSimOrEmu mirrors internal/mcp/tools.go's
// isSimulatorOrEmulator classification (🎯T98): --all-physical fan-out
// must only target physical hardware, never simulators/emulators.
func TestLooksLikeFanOutSimOrEmu(t *testing.T) {
	cases := []struct {
		name string
		d    fanOutDevice
		want bool
	}{
		{
			name: "ios simulator standard UUID",
			d:    fanOutDevice{UUID: "A1B2C3D4-E5F6-7890-ABCD-1234567890AB", Platform: "ios"},
			want: true,
		},
		{
			name: "ios physical 8+16 hex UDID",
			d:    fanOutDevice{UUID: "00008110-000A59C13C29001E", Platform: "ios"},
			want: false,
		},
		{
			name: "android emulator serial",
			d:    fanOutDevice{UUID: "emulator-5554", Platform: "android"},
			want: true,
		},
		{
			name: "android physical serial",
			d:    fanOutDevice{UUID: "R58N90ABCDE", Platform: "android"},
			want: false,
		},
		{
			name: "desktop platform never classified sim/emu here (caller filters by platform)",
			d:    fanOutDevice{UUID: "whatever", Platform: "desktop"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeFanOutSimOrEmu(tc.d); got != tc.want {
				t.Errorf("looksLikeFanOutSimOrEmu(%+v) = %v, want %v", tc.d, got, tc.want)
			}
		})
	}
}
