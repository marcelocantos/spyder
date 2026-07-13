// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"testing"
	"time"
)

// base is the fixed "now" clock used across all Classify scenario rows.
// Using a Unix timestamp makes the arithmetic in Evidence construction
// unambiguous and keeps test output reproducible.
var base = time.Unix(1_000_000, 0)

// layers builds the standard 4-layer iOS device stack — usbmux, pairing,
// tunnel, dtx — in parent→child order. ok specifies the OK flag for each
// layer in the same order; all four layers are marked Observed=true.
func layers(usbmuxOK, pairingOK, tunnelOK, dtxOK bool) []LayerState {
	return []LayerState{
		{Layer: "usbmux", OK: usbmuxOK, Observed: true},
		{Layer: "pairing", OK: pairingOK, Observed: true},
		{Layer: "tunnel", OK: tunnelOK, Observed: true},
		{Layer: "dtx", OK: dtxOK, Observed: true},
	}
}

// oracle builds a fully-observed OracleReading (Observed=true).
func oracle(source string, present bool) OracleReading {
	return OracleReading{Source: source, Present: present, Observed: true}
}

func TestClassify(t *testing.T) {
	t.Parallel()

	type expect struct {
		class          FaultClass
		suggestedState State
		confidence     Confidence
		urgent         bool
		needsAttention bool
	}

	rows := []struct {
		name string
		e    Evidence
		want expect
	}{
		{
			// Row 1 — all layers healthy, oracles corroborate presence.
			name: "healthy",
			e: Evidence{
				Layers: layers(true, true, true, true),
				Oracles: []OracleReading{
					oracle("usbmux", true),
					oracle("devicectl", true),
				},
				Now: base,
			},
			want: expect{
				class:          ClassHealthy,
				suggestedState: Healthy,
				confidence:     ConfidenceHigh,
				urgent:         false,
				needsAttention: false,
			},
		},
		{
			// Row 2 — tunnel and dtx are down while usbmux+pairing are up:
			// recoverable spyder-side fault, not a physical event.
			name: "recoverable tunnel",
			e: Evidence{
				Layers: layers(true, true, false, false),
				Oracles: []OracleReading{
					oracle("usbmux", true),
					oracle("devicectl", true),
				},
				Now: base,
			},
			want: expect{
				class:          ClassRecoverableTunnel,
				suggestedState: Degraded,
				confidence:     ConfidenceHigh,
				urgent:         false,
				needsAttention: false,
			},
		},
		{
			// Row 3 — detach with no subsequent attach; all 3 oracles
			// corroborate that the device is gone; device is not pinned.
			name: "physical removal non-pinned",
			e: Evidence{
				Layers: layers(false, false, false, false),
				Oracles: []OracleReading{
					oracle("usbmux", false),
					oracle("devicectl", false),
					oracle("iousb", false),
				},
				LastDetach:      base.Add(-1 * time.Second),
				ExpectedPresent: false,
				Now:             base,
			},
			want: expect{
				class:          ClassPhysicalRemoval,
				suggestedState: AbsentUnexpected,
				confidence:     ConfidenceHigh,
				urgent:         false,
				needsAttention: false,
			},
		},
		{
			// Row 4 — same as row 3 but the device is pinned (ExpectedPresent).
			// NeedsAttention() must be true because SuggestedState==NeedsAttention.
			// Urgent must be false because physical removal is not in the urgent set.
			name: "physical removal pinned",
			e: Evidence{
				Layers: layers(false, false, false, false),
				Oracles: []OracleReading{
					oracle("usbmux", false),
					oracle("devicectl", false),
					oracle("iousb", false),
				},
				LastDetach:      base.Add(-1 * time.Second),
				ExpectedPresent: true,
				Now:             base,
			},
			want: expect{
				class:          ClassPhysicalRemoval,
				suggestedState: NeedsAttention,
				confidence:     ConfidenceHigh,
				urgent:         false, // physical removal never sets Urgent
				needsAttention: true,  // derived from SuggestedState==NeedsAttention
			},
		},
		{
			// Row 5 — detach 2 s ago, attach 1 s ago: the gap (1 s) and the
			// time since attach (1 s) are both within reenumWindow (5 s).
			// Tunnel is still settling (!OK) but that is irrelevant — the
			// re-enumeration rule wins.
			name: "re-enumeration",
			e: Evidence{
				Layers:     layers(true, true, false, false),
				LastDetach: base.Add(-2 * time.Second),
				LastAttach: base.Add(-1 * time.Second),
				Now:        base,
			},
			want: expect{
				class:          ClassReenumerating,
				suggestedState: Recovering,
				confidence:     ConfidenceHigh,
				urgent:         false,
				needsAttention: false,
			},
		},
		{
			// Row 6 — no detach; all layers appear OK at the layer level; but
			// usbmux oracle says present while devicectl says absent. Looks
			// like a stack wedge.
			name: "oracle disagreement (wedge)",
			e: Evidence{
				Layers: layers(true, true, true, true),
				Oracles: []OracleReading{
					oracle("usbmux", true),
					oracle("devicectl", false),
					oracle("iousb", true),
				},
				Now: base,
			},
			want: expect{
				class:          ClassStackInconsistency,
				suggestedState: Degraded,
				confidence:     ConfidenceLow,
				urgent:         false,
				needsAttention: false,
			},
		},
		{
			// Row 7 — detach with no attach; all 3 oracles observed and Present=false.
			// Identical to row 3 in outcome but confirms allGone → ConfidenceHigh.
			name: "physical removal high-confidence",
			e: Evidence{
				Layers: layers(false, false, false, false),
				Oracles: []OracleReading{
					oracle("usbmux", false),
					oracle("devicectl", false),
					oracle("iousb", false),
				},
				LastDetach:      base.Add(-1 * time.Second),
				ExpectedPresent: false,
				Now:             base,
			},
			want: expect{
				class:          ClassPhysicalRemoval,
				suggestedState: AbsentUnexpected,
				confidence:     ConfidenceHigh,
				urgent:         false,
				needsAttention: false,
			},
		},
		{
			// Row 8 — same as row 2 (recoverable tunnel) but the device is
			// Reserved. Urgent must flip to true because the supervisor can
			// self-heal this fault and the agent is actively using the device.
			name: "recoverable tunnel on reserved device",
			e: Evidence{
				Layers: layers(true, true, false, false),
				Oracles: []OracleReading{
					oracle("usbmux", true),
					oracle("devicectl", true),
				},
				Reserved: true,
				Now:      base,
			},
			want: expect{
				class:          ClassRecoverableTunnel,
				suggestedState: Degraded,
				confidence:     ConfidenceHigh,
				urgent:         true,
				needsAttention: false,
			},
		},
		{
			// Row 9 — detach 1 s ago with no attach; usbmux oracle says present
			// but devicectl says absent. The detach is contradicted by an oracle
			// → stack inconsistency, not physical removal.
			name: "detach contradicted by oracles",
			e: Evidence{
				Layers: layers(false, false, false, false),
				Oracles: []OracleReading{
					oracle("usbmux", true),
					oracle("devicectl", false),
				},
				LastDetach: base.Add(-1 * time.Second),
				Now:        base,
			},
			want: expect{
				class:          ClassStackInconsistency,
				suggestedState: Degraded,
				confidence:     ConfidenceLow,
				urgent:         false,
				needsAttention: false,
			},
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			a := Classify(row.e)

			if a.Class != row.want.class {
				t.Errorf("Class: got %q, want %q", a.Class, row.want.class)
			}
			if a.SuggestedState != row.want.suggestedState {
				t.Errorf("SuggestedState: got %q, want %q", a.SuggestedState, row.want.suggestedState)
			}
			if a.Confidence != row.want.confidence {
				t.Errorf("Confidence: got %q, want %q", a.Confidence, row.want.confidence)
			}
			if a.Urgent != row.want.urgent {
				t.Errorf("Urgent: got %v, want %v", a.Urgent, row.want.urgent)
			}
			if a.NeedsAttention() != row.want.needsAttention {
				t.Errorf("NeedsAttention(): got %v, want %v", a.NeedsAttention(), row.want.needsAttention)
			}
			if len(a.Evidence) == 0 {
				t.Error("Evidence must be non-empty: assessment must explain itself")
			}
		})
	}
}
