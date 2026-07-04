// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"testing"
	"time"
)

// TestLogsEndToEnd_Tiltbuggy is a 🎯T91.3 live oracle: a real direct-mode ge
// app streams its structured logs to spyder over the app-channel — ged's
// `logs` capability, served without ged. The ge log sink forwards to
// SPYDER_APP_CHANNEL (src/log.cpp), so spyder's per-session log buffer fills
// as the app runs; DrainLogs is what `app_log_get` surfaces to an agent.
//
// Gated on SPYDER_GE_TILTBUGGY (launches a real GUI app); skips otherwise.
func TestLogsEndToEnd_Tiltbuggy(t *testing.T) {
	bin := requireTiltbuggy(t)
	s, cleanup := launchTiltbuggy(t, bin)
	defer cleanup()

	// The app emits startup + per-frame logs; drain until some arrive.
	var logs []LogPush
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.DrainLogs()
		logs = append(logs, got...)
		if len(logs) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(logs) == 0 {
		t.Fatal("no logs streamed from tiltbuggy over the app-channel")
	}
	// Entries must be structured (a level and/or a format string), matching
	// ged's log shape after normalization.
	if logs[0].Level == "" && logs[0].Format == "" {
		t.Fatalf("log entry is unstructured: %+v", logs[0])
	}
	t.Logf("streamed %d log line(s) over the app-channel; first: [%s] %s",
		len(logs), logs[0].Level, logs[0].Format)
}
