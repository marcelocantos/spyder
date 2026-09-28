// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ownerReviewsFile is an export of a run's owner reviews in its bundle, so a
// shared bundle is self-contained. The review database is the source of truth.
const ownerReviewsFile = "owner-reviews.json"

// OwnerReview is the owner's finding and notes on one step, saved from the
// dashboard as they type. It is review evidence beside the run; it does not
// change the run's status. Finding may be empty while only notes exist.
type OwnerReview struct {
	Finding   string    `json:"finding,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FindingInGame says the owner cannot judge the entry from the evidence and
// must see it in the running product. It is neither pass nor fail: the entry
// is assessed for this review, and counted as needing an in-game check.
const FindingInGame = "in_game"

// inGameFinding is offered on every reviewable entry after its own choices.
var inGameFinding = Choice{ID: FindingInGame, Label: "Check in game"}

// modelStepFindings are the owner's findings on a model step's result.
var modelStepFindings = []Choice{
	{ID: "pass", Label: "Pass"},
	{ID: "fail", Label: "Fail"},
	{ID: "unclear", Label: "Unclear"},
}

// reviewOptions returns the findings the owner can record for a step, or nil
// when the step does not await owner review: only model results and owner
// gates left pending (deferred) do. A gate offers its own choices, so a
// finding means what an owner answer would.
func reviewOptions(s Step, status string) []Choice {
	var options []Choice
	switch {
	case s.Type == KindHumanGate && status == StepDeferred:
		options = s.Choices
	case s.Type == KindModel && (status == StepOK || status == StepFailed):
		options = modelStepFindings
	default:
		return nil
	}
	for _, c := range options {
		if c.ID == FindingInGame {
			return options
		}
	}
	return append(append([]Choice{}, options...), inGameFinding)
}

// reviewComplete reports whether a review settles its step: it has a finding,
// plus notes when that finding requires a comment.
func reviewComplete(rev *OwnerReview, options []Choice) bool {
	if rev == nil || rev.Finding == "" {
		return false
	}
	for _, c := range options {
		if c.ID == rev.Finding {
			return c.Comment != "required" || strings.TrimSpace(rev.Notes) != ""
		}
	}
	return false
}

// stepByRef finds a step by ID or outline number ("1.2.3").
func (r *Run) stepByRef(ref string) (Step, bool) {
	outline := outlineOf(r.wf)
	for _, s := range append(append([]Step{}, r.wf.Steps...), r.wf.Cleanup...) {
		if s.ID == ref || outline.Steps[s.ID] == ref {
			return s, true
		}
	}
	return Step{}, false
}

// Review saves the owner's finding and notes on a step, replacing any earlier
// save; empty finding and notes clear it. Saves are partial-friendly because
// the dashboard saves while the owner types.
func (h *Hub) Review(runID, stepRef, finding, notes string) (*OwnerReview, error) {
	run := h.RunByID(runID)
	if run == nil {
		return nil, fmt.Errorf("unknown run %s", runID)
	}
	step, ok := run.stepByRef(stepRef)
	if !ok {
		return nil, fmt.Errorf("run %s has no step %s", runID, stepRef)
	}
	run.mu.Lock()
	options := reviewOptions(step, run.stepStatus[step.ID])
	status := run.stepStatus[step.ID]
	previous := run.reviews[step.ID]
	run.mu.Unlock()
	if options == nil {
		return nil, fmt.Errorf("step %s is not awaiting owner review (status %s)", step.ID, status)
	}
	if finding != "" {
		known := false
		ids := make([]string, len(options))
		for i, c := range options {
			ids[i] = c.ID
			known = known || c.ID == finding
		}
		if !known {
			return nil, fmt.Errorf("finding %q is not one of %s for step %s", finding, strings.Join(ids, ", "), step.ID)
		}
	}
	var rev *OwnerReview
	if finding != "" || notes != "" {
		rev = &OwnerReview{Finding: finding, Notes: notes, UpdatedAt: time.Now().UTC()}
	}
	if err := h.reviews.put(run.ID, step.ID, rev); err != nil {
		return nil, fmt.Errorf("save owner review: %w", err)
	}
	run.mu.Lock()
	if rev == nil {
		delete(run.reviews, step.ID)
	} else {
		run.reviews[step.ID] = rev
	}
	run.exportReviewsLocked()
	if prevFinding := findingOf(previous); prevFinding != finding {
		line := "owner review " + step.ID + "  finding=" + finding
		if finding == "" {
			line = "owner review " + step.ID + "  finding cleared"
		}
		run.appendEventLocked(line)
	}
	run.mu.Unlock()
	h.notify()
	return rev, nil
}

func findingOf(rev *OwnerReview) string {
	if rev == nil {
		return ""
	}
	return rev.Finding
}

// exportReviewsLocked refreshes the bundle's owner-reviews.json. Callers hold
// r.mu. The database already holds the review, so a failed export only costs
// the bundle copy.
func (r *Run) exportReviewsLocked() {
	data, err := json.MarshalIndent(r.reviews, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(r.reportDir, ownerReviewsFile)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, append(data, '\n'), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// appendEventLocked adds a line to the event log and live log after the run
// may have finished. Callers hold r.mu.
func (r *Run) appendEventLocked(line string) {
	if f, err := os.OpenFile(filepath.Join(r.reportDir, "events.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		_, _ = fmt.Fprintln(f, line)
		_ = f.Close()
	}
	r.logs = append(r.logs, line)
}
