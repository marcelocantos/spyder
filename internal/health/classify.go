// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"fmt"
	"time"
)

// reenumWindow is the maximum interval between a detach and a subsequent
// attach that we still classify as a benign re-enumeration (USB re-plug or
// OS-level re-enumeration) rather than a physical removal followed by a new
// connection. Tunable so tests and field ops can adjust without recompiling.
var reenumWindow = 5 * time.Second

// LayerState is the observed state of one device stack layer.
type LayerState struct {
	Layer    string // "usbmux","pairing","tunnel","dtx" — ordered parent→child
	OK       bool   // layer is up/working (only meaningful when Observed)
	Observed bool   // did we actually probe this layer? unknown vs. down
}

// OracleReading is one corroborating source's view of device presence.
type OracleReading struct {
	Source   string // "usbmux","devicectl","iousb"
	Present  bool
	Observed bool
}

// Evidence is the complete, pure input to Classify. No I/O happens inside
// Classify; the supervisor gathers this snapshot and passes it in.
// Using Evidence.Now rather than time.Now keeps the classifier a pure function
// that can be exhaustively table-tested without mocking a global clock.
type Evidence struct {
	UDID            string
	Layers          []LayerState    // per stack layer, ordered parent→child
	Oracles         []OracleReading // corroborating presence sources
	LastDetach      time.Time       // most recent usbmux detach; zero if none
	LastAttach      time.Time       // most recent usbmux attach; zero if none
	Now             time.Time
	Reserved        bool // device is reserved / in an active operation
	ExpectedPresent bool // pinned: developer marked it must-be-connected
}

// FaultClass is the classifier's primary verdict — what kind of problem (or
// non-problem) explains the observed evidence.
type FaultClass string

const (
	// ClassHealthy means all probed layers are up and oracles agree the device
	// is present. No action required.
	ClassHealthy FaultClass = "healthy"

	// ClassReenumerating means a detach→attach occurred within reenumWindow:
	// the device is doing a benign USB re-enumeration and will be back.
	ClassReenumerating FaultClass = "reenumerating"

	// ClassRecoverableTunnel means a child layer went down while its parent is
	// still up — a spyder-side failure the supervisor can self-heal by
	// restarting the affected layer.
	ClassRecoverableTunnel FaultClass = "recoverable_tunnel"

	// ClassPhysicalRemoval means the device has left the USB bus (usbmux
	// detach and/or all oracles agree it is gone).
	ClassPhysicalRemoval FaultClass = "physical_removal"

	// ClassStackInconsistency means the oracles disagree about device presence —
	// a sign of a stack wedge rather than a physical event.
	ClassStackInconsistency FaultClass = "stack_inconsistency"
)

// Confidence describes how certain the classifier is about its verdict.
type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Assessment is the classifier's output — always carries a confidence and
// supporting evidence, never a bare boolean. The supervisor feeds this into
// the health model rather than deciding transitions ad hoc.
type Assessment struct {
	Class          FaultClass
	Confidence     Confidence
	SuggestedState State    // the T90.1 state the supervisor should move toward
	Urgent         bool     // reserved/in-operation → surface to the agent sooner
	Evidence       []string // human-readable supporting evidence, one line per rule that fired
}

// NeedsAttention reports whether this assessment warrants a human push
// (T90.4). Derived purely from SuggestedState so the caller never has to
// compare string constants.
func (a Assessment) NeedsAttention() bool { return a.SuggestedState == NeedsAttention }

// Classify is a pure function: given a fully-populated Evidence snapshot it
// returns an Assessment. No I/O, no time.Now, no global mutable state — the
// purity is deliberate so that every branch can be exercised by a table test
// without mocking.
//
// Rules are applied in priority order; the first matching rule sets the Class.
// Evidence strings accumulate across all rules that fire.
func Classify(e Evidence) Assessment {
	var ev []string

	// ── Rule 1: Re-enumeration coalescing ────────────────────────────────────
	// If we have both a detach and a subsequent attach that are close together
	// in time, treat the whole event as a benign USB re-enumeration. This is
	// the highest-priority rule so a re-plugged device is never misclassified
	// as a removal.
	if !e.LastDetach.IsZero() && !e.LastAttach.IsZero() &&
		e.LastAttach.After(e.LastDetach) &&
		(e.Now.Sub(e.LastAttach) <= reenumWindow ||
			e.LastAttach.Sub(e.LastDetach) <= reenumWindow) {

		sinceAttach := e.Now.Sub(e.LastAttach)
		ev = append(ev, fmt.Sprintf(
			"detach→attach within %.0fs: treating as re-enumeration", sinceAttach.Seconds()))
		return Assessment{
			Class:          ClassReenumerating,
			Confidence:     ConfidenceHigh,
			SuggestedState: Recovering,
			Evidence:       ev,
		}
	}

	// ── Rule 2: Physical removal via authoritative detach ─────────────────────
	// A detach with no newer attach is the primary signal that the device left
	// the bus. We then consult the oracles to calibrate confidence and check
	// for contradictions that would indicate a stack wedge instead.
	if !e.LastDetach.IsZero() &&
		(e.LastAttach.IsZero() || e.LastAttach.Before(e.LastDetach)) {

		observedOracles := observedOracleReadings(e.Oracles)
		anyPresent := anyOraclePresent(observedOracles)
		anyGone := anyOracleGone(observedOracles)
		allGone := len(observedOracles) > 0 && !anyPresent

		if len(observedOracles) > 0 && anyPresent && anyGone {
			// Oracles disagree: usbmux says detach but at least one oracle
			// still sees the device. Likely a stack wedge, not a physical removal.
			ev = append(ev, "usbmux detach but oracles disagree: possible stack wedge")
			return Assessment{
				Class:          ClassStackInconsistency,
				Confidence:     ConfidenceLow,
				SuggestedState: Degraded,
				Evidence:       ev,
			}
		}

		// Oracles either all agree it's gone, or none were observed.
		confidence := ConfidenceMedium
		if allGone {
			// Every observed oracle corroborates the detach — high confidence.
			confidence = ConfidenceHigh
		}

		suggestedState := AbsentUnexpected
		if e.ExpectedPresent {
			suggestedState = NeedsAttention
		}

		ev = append(ev, "usbmux detach; device physically removed")
		if e.ExpectedPresent {
			ev = append(ev, "pinned device absent — needs reconnect")
		}

		a := Assessment{
			Class:          ClassPhysicalRemoval,
			Confidence:     confidence,
			SuggestedState: suggestedState,
			Evidence:       ev,
		}
		// Physical removal is not in the Urgent set — Urgent applies only to
		// faults on actively-used devices where spyder can self-heal.
		return applyUrgency(a, e)
	}

	// ── Rule 3: Layer localisation (no authoritative detach) ─────────────────
	// Walk the layer stack parent→child looking for the first layer that is
	// observed-and-down while its parent is observed-and-up. A child dying
	// while its parent lives is a spyder-side fault (e.g. the tunnel process
	// crashed) that the supervisor can restart without user involvement.
	if fault, parent := firstFaultedLayer(e.Layers); fault != nil {
		if fault.Layer == "usbmux" {
			// usbmux is the root; if it's down without a detach event, we treat
			// it as a physical removal (usbmux can't see the device at all).
			suggestedState := AbsentUnexpected
			if e.ExpectedPresent {
				suggestedState = NeedsAttention
			}
			ev = append(ev, "usbmux layer down")
			a := Assessment{
				Class:          ClassPhysicalRemoval,
				Confidence:     ConfidenceMedium,
				SuggestedState: suggestedState,
				Evidence:       ev,
			}
			return applyUrgency(a, e)
		}

		// A non-root layer is down while its parent is up — recoverable.
		_ = parent // parent is the preceding observed-OK layer; used for diagnosis
		ev = append(ev, fmt.Sprintf(
			"layer %q down while parent up: recoverable spyder-side fault, not a physical disconnect",
			fault.Layer))
		a := Assessment{
			Class:          ClassRecoverableTunnel,
			Confidence:     ConfidenceHigh,
			SuggestedState: Degraded,
			Evidence:       ev,
		}
		return applyUrgency(a, e)
	}

	// ── Rule 4: Oracle disagreement without any layer fault ───────────────────
	// All probed layers are up but the oracles disagree — something in the
	// platform stack has an inconsistent view. This is a wedge scenario.
	observedOracles := observedOracleReadings(e.Oracles)
	if anyOraclePresent(observedOracles) && anyOracleGone(observedOracles) {
		ev = append(ev, "oracles disagree on device presence: stack inconsistency")
		a := Assessment{
			Class:          ClassStackInconsistency,
			Confidence:     ConfidenceLow,
			SuggestedState: Degraded,
			Evidence:       ev,
		}
		return applyUrgency(a, e)
	}

	// ── Rule 5: Healthy ───────────────────────────────────────────────────────
	ev = append(ev, "all observed layers up; oracles agree present")
	return Assessment{
		Class:          ClassHealthy,
		Confidence:     ConfidenceHigh,
		SuggestedState: Healthy,
		Evidence:       ev,
	}
}

// applyUrgency sets Urgent when the device is Reserved AND the fault class is
// one the supervisor can self-heal ({RecoverableTunnel, StackInconsistency}).
// Physical removals are excluded: there is nothing the supervisor can do for
// a device that has left the bus, so urgency adds no value.
func applyUrgency(a Assessment, e Evidence) Assessment {
	if e.Reserved &&
		(a.Class == ClassRecoverableTunnel || a.Class == ClassStackInconsistency) {
		a.Urgent = true
		a.Evidence = append(a.Evidence,
			"device reserved/in active operation: escalating to agent")
	}
	return a
}

// observedOracleReadings returns only the oracles that were actually probed
// (Observed==true). Unobserved oracles provide no information.
func observedOracleReadings(oracles []OracleReading) []OracleReading {
	out := make([]OracleReading, 0, len(oracles))
	for _, o := range oracles {
		if o.Observed {
			out = append(out, o)
		}
	}
	return out
}

// anyOraclePresent reports whether any oracle in the slice has Present==true.
func anyOraclePresent(oracles []OracleReading) bool {
	for _, o := range oracles {
		if o.Present {
			return true
		}
	}
	return false
}

// anyOracleGone reports whether any oracle in the slice has Present==false.
func anyOracleGone(oracles []OracleReading) bool {
	for _, o := range oracles {
		if !o.Present {
			return true
		}
	}
	return false
}

// firstFaultedLayer walks layers parent→child and returns the first layer
// that is (Observed && !OK) which is either the root (no observed parent) or
// whose immediate observed parent is up, plus that parent layer (nil for the
// root). Returns (nil, nil) when no such fault exists.
//
// A down layer whose parent is ALSO down is skipped: the fault belongs to the
// parent (returned on an earlier iteration or itself downstream of a higher
// failure), so we localise to the highest broken layer rather than every
// layer below it.
func firstFaultedLayer(layers []LayerState) (fault *LayerState, parent *LayerState) {
	var prevObserved *LayerState // most recent Observed layer, OK or not
	for i := range layers {
		l := &layers[i]
		if !l.Observed {
			continue
		}
		if !l.OK && (prevObserved == nil || prevObserved.OK) {
			return l, prevObserved
		}
		prevObserved = l
	}
	return nil, nil
}
