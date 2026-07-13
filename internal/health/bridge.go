// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"fmt"
	"strings"
)

// ApplyAssessment drives the model for a device entity from a classifier
// Assessment, translating the SuggestedState into the model's transition
// vocabulary so device faults flow through the SAME state machine — and
// thus the same status surface (T90.3) and notifier (T90.4) — as every
// other entity. id should be the device entity (Kind=KindDevice, Name=UDID,
// Layer e.g. "tunnel").
//
// The mapping is deliberate: the classifier's SuggestedState is a semantic
// label (what SHOULD the state be), while the model's methods are transition
// verbs (what IS HAPPENING). This bridge converts one vocabulary into the
// other so the supervisor never has to reason about model internals.
func ApplyAssessment(m *Model, id ID, a Assessment) {
	detail := assessmentDetail(a)
	switch a.SuggestedState {
	case Healthy:
		m.Observe(id, true, detail)
	case Degraded:
		m.Observe(id, false, detail)
	case Recovering:
		// Two-step: record the failure first, then signal that a recovery
		// attempt has started. Observe(false) only advances from
		// Healthy/Absent* to Degraded; it is a no-op from Degraded/Recovering.
		m.Observe(id, false, detail)
		m.RecoveryStarted(id)
	case NeedsAttention:
		m.MarkNeedsAttention(id, detail)
	case AbsentExpected:
		m.MarkAbsent(id, true, detail)
	case AbsentUnexpected:
		m.MarkAbsent(id, false, detail)
	default:
		// Unknown suggested state — conservative fallback: record as a failure
		// so the entity surfaces in the pull view without escalating further.
		m.Observe(id, false, detail)
	}
}

// assessmentDetail returns a compact human-readable line that summarises the
// assessment for use as the model's Observation.Detail. It combines the fault
// class, confidence, and the joined evidence lines so a single string captures
// everything the classifier saw — useful when browsing entity snapshots.
//
// Example: `"recoverable_tunnel (high): layer \"tunnel\" down while parent up"`
func assessmentDetail(a Assessment) string {
	base := fmt.Sprintf("%s (%s)", a.Class, a.Confidence)
	if len(a.Evidence) == 0 {
		return base
	}
	return base + ": " + strings.Join(a.Evidence, "; ")
}
