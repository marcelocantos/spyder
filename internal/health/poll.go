// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"time"
)

// Prober produces liveness observations for a poll cycle. Implementations
// are injected so the poll loop is testable with a fake prober that
// performs no real device or host I/O.
type Prober interface {
	Probe() []ProbeResult
}

// ProbeResult is one entity's liveness reading from a single Prober.Probe
// call. When Absent is true, Expected indicates whether the absence is
// benign (planned disconnect) or surprising.
type ProbeResult struct {
	ID       ID
	OK       bool // used when Absent == false
	Absent   bool // true → entity is gone; use Expected
	Expected bool // for Absent: true = benign, false = surprising
	Detail   string
}

// RunPoll applies prober results to the model on every interval tick until
// ctx is cancelled. This is the low-frequency, background liveness source
// described in 🎯T90.1 ("driven by BOTH events and a low-frequency poll").
// Event-driven transitions (attach/detach, subprocess exit) drive the model
// directly via Observe/MarkAbsent; RunPoll is the fallback that catches
// silent failures that emit no events.
//
// The first probe fires immediately on entry (no leading wait), so callers
// see model updates within milliseconds rather than waiting a full interval.
func (m *Model) RunPoll(ctx context.Context, interval time.Duration, prober Prober) {
	apply := func() {
		for _, r := range prober.Probe() {
			if r.Absent {
				m.MarkAbsent(r.ID, r.Expected, r.Detail)
			} else {
				m.Observe(r.ID, r.OK, r.Detail)
			}
		}
	}

	// Probe immediately so the model reflects reality before the first tick.
	apply()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			apply()
		}
	}
}
