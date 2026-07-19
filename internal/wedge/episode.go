// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import "strings"

// ClassifyTunnelStartWedge detects the 2026-07-16 usbmuxd broken-pipe /
// tunnel-start-loop class from log lines (🎯T99.4 class-1 oracle).
// Pure function — no I/O.
func ClassifyTunnelStartWedge(logLines []string) (isWedge bool, detail string) {
	var brokenPipe, tunnelStart int
	for _, ln := range logLines {
		l := strings.ToLower(ln)
		if strings.Contains(l, "broken pipe") && (strings.Contains(l, "usbmux") || strings.Contains(l, "/var/run/usbmuxd")) {
			brokenPipe++
		}
		if strings.Contains(l, "failed to start tunnel") {
			tunnelStart++
		}
	}
	if brokenPipe >= 2 && tunnelStart >= 1 {
		return true, "usbmuxd broken-pipe / tunnel-start-loop"
	}
	if brokenPipe >= 3 {
		return true, "usbmuxd broken-pipe sustained"
	}
	return false, ""
}
