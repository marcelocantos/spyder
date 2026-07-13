// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
)

// TestFormatStatus verifies the `spyder status` table renders the injected
// entities: one line per entity (KIND/NAME[/LAYER]), the state, attempt
// count, last-probe, and the most recent evidence detail — grouped/sorted by
// Kind then Name. Pure function, no daemon.
func TestFormatStatus(t *testing.T) {
	probe := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	snap := health.Snapshot{
		At: probe,
		Entities: []health.EntitySnapshot{
			{
				ID:        health.ID{Kind: health.KindDaemon, Name: "spyderd"},
				Kind:      health.KindDaemon,
				State:     health.Healthy,
				LastProbe: probe,
				Evidence:  []health.Observation{{At: probe, OK: true, Detail: "serving"}},
			},
			{
				ID:        health.ID{Kind: health.KindDevice, Name: "iPad", Layer: "usbmux"},
				Kind:      health.KindDevice,
				State:     health.AbsentUnexpected,
				LastProbe: probe,
				Evidence:  []health.Observation{{At: probe, OK: false, Detail: "unplugged"}},
			},
			{
				ID:       health.ID{Kind: health.KindSubprocess, Name: "ios-tunnel"},
				Kind:     health.KindSubprocess,
				State:    health.Degraded,
				Attempts: 2,
				// No last-probe / evidence: exercises the "never" + no-detail path.
			},
		},
	}

	out := formatStatus(snap)

	for _, want := range []string{
		"daemon/spyderd",
		"healthy",
		"serving",
		"device/iPad/usbmux",
		"absent_unexpected",
		"unplugged",
		"subprocess/ios-tunnel",
		"degraded",
		"attempts=2",
		"last_probe=never",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatStatus output missing %q\n---\n%s", want, out)
		}
	}

	// Sorted by Kind: daemon < device < subprocess.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "daemon/") ||
		!strings.HasPrefix(lines[1], "device/") ||
		!strings.HasPrefix(lines[2], "subprocess/") {
		t.Errorf("lines not sorted by kind:\n%s", out)
	}
}

// TestFormatStatus_Empty renders a clear message when nothing is monitored.
func TestFormatStatus_Empty(t *testing.T) {
	out := formatStatus(health.Snapshot{})
	if !strings.Contains(out, "no monitored entities") {
		t.Errorf("empty snapshot output = %q", out)
	}
}
