// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import "testing"

func TestClassifyTunnelStartWedge_Synthetic(t *testing.T) {
	lines := []string{
		`write unix ->/var/run/usbmuxd: broken pipe`,
		`failed to start tunnel for device A`,
		`write unix ->/var/run/usbmuxd: broken pipe`,
		`failed to start tunnel for device B`,
	}
	ok, detail := ClassifyTunnelStartWedge(lines)
	if !ok {
		t.Fatal("expected wedge")
	}
	if detail == "" {
		t.Fatal("empty detail")
	}
}

func TestClassifyTunnelStartWedge_Noise(t *testing.T) {
	ok, _ := ClassifyTunnelStartWedge([]string{"all fine", "tunnel start ok"})
	if ok {
		t.Fatal("noise should not classify as wedge")
	}
}
