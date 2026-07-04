// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"strings"
	"testing"
)

// TestApplyAssessment is a table-driven test that applies an Assessment with
// each possible SuggestedState to a fresh model and asserts that the entity
// lands in the expected state. It also verifies that the observation detail
// recorded in the model is non-empty and mentions the fault class — so the
// pull surface (T90.3) shows human-readable context alongside every state.
func TestApplyAssessment(t *testing.T) {
	cases := []struct {
		name          string
		assessment    Assessment
		expectedState State
		// preFn, if set, runs on the model/id BEFORE ApplyAssessment so the
		// entity is in a specific state (e.g. already Degraded) when the bridge
		// fires — needed for Recovering which calls RecoveryStarted.
		preFn func(m *Model, id ID)
	}{
		{
			name: "Healthy",
			assessment: Assessment{
				Class:          ClassHealthy,
				Confidence:     ConfidenceHigh,
				SuggestedState: Healthy,
				Evidence:       []string{"all observed layers up; oracles agree present"},
			},
			expectedState: Healthy,
		},
		{
			name: "Degraded",
			assessment: Assessment{
				Class:          ClassRecoverableTunnel,
				Confidence:     ConfidenceHigh,
				SuggestedState: Degraded,
				Evidence:       []string{`layer "tunnel" down while parent up`},
			},
			expectedState: Degraded,
		},
		{
			name: "Recovering",
			assessment: Assessment{
				Class:          ClassReenumerating,
				Confidence:     ConfidenceHigh,
				SuggestedState: Recovering,
				Evidence:       []string{"detach→attach within 3s: treating as re-enumeration"},
			},
			expectedState: Recovering,
		},
		{
			name: "NeedsAttention",
			assessment: Assessment{
				Class:          ClassPhysicalRemoval,
				Confidence:     ConfidenceHigh,
				SuggestedState: NeedsAttention,
				Evidence:       []string{"usbmux detach; device physically removed", "pinned device absent — needs reconnect"},
			},
			expectedState: NeedsAttention,
		},
		{
			name: "AbsentExpected",
			assessment: Assessment{
				Class:          ClassPhysicalRemoval,
				Confidence:     ConfidenceHigh,
				SuggestedState: AbsentExpected,
				Evidence:       []string{"usbmux detach; device physically removed"},
			},
			expectedState: AbsentExpected,
		},
		{
			name: "AbsentUnexpected",
			assessment: Assessment{
				Class:          ClassPhysicalRemoval,
				Confidence:     ConfidenceMedium,
				SuggestedState: AbsentUnexpected,
				Evidence:       []string{"usbmux detach; device physically removed"},
			},
			expectedState: AbsentUnexpected,
		},
		{
			name: "unknown_state_defaults_to_Degraded",
			assessment: Assessment{
				Class:          ClassStackInconsistency,
				Confidence:     ConfidenceLow,
				SuggestedState: State("unknown_future_state"),
				Evidence:       []string{"some novel evidence"},
			},
			// Unknown SuggestedState falls through to Observe(false) from
			// Healthy, which lands on Degraded.
			expectedState: Degraded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestModel()
			id := ID{Kind: KindDevice, Name: "test-udid", Layer: "tunnel"}
			m.Register(id, KindDevice, Policy{MaxAttempts: 5})

			if tc.preFn != nil {
				tc.preFn(m, id)
			}

			ApplyAssessment(m, id, tc.assessment)

			snap, ok := m.Get(id)
			if !ok {
				t.Fatal("entity not found after ApplyAssessment")
			}
			if snap.State != tc.expectedState {
				t.Errorf("state: want %s, got %s", tc.expectedState, snap.State)
			}

			// The detail recorded in the model's evidence must be non-empty
			// and must mention the fault class name so snapshots are self-describing.
			if len(snap.Evidence) == 0 {
				t.Fatal("no evidence recorded after ApplyAssessment")
			}
			lastDetail := snap.Evidence[len(snap.Evidence)-1].Detail
			if lastDetail == "" {
				t.Error("last evidence.Detail is empty")
			}
			if !strings.Contains(lastDetail, string(tc.assessment.Class)) {
				t.Errorf("detail %q does not mention fault class %q", lastDetail, tc.assessment.Class)
			}
		})
	}
}
