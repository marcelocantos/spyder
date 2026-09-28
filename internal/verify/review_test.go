// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reviewWorkflow = `
name: review-sample
steps:
  - id: build
    type: shell
    argv: [/usr/bin/true]
  - id: screen_check
    type: model
    requires: [build]
    prompt: Is it right?
    model: {purpose: analysis, quality: standard}
  - id: looks
    type: human_gate
    requires: [screen_check]
    prompt: Does it look right?
    choices:
      - {id: pass, label: "Yes"}
      - {id: fail, label: "No", outcome: investigate, comment: required}
`

// Owner reviews of model results and deferred gates save partially as the
// owner types, persist in the review database across restarts, and feed the
// pending-review count, without changing the run status.
func TestOwnerReviewSavesAsTypedAndSurvivesRestart(t *testing.T) {
	state := t.TempDir()
	model := func(context.Context, StepRequest) ExecResult {
		return ExecResult{Code: 0, Output: "PASS", Model: &ModelEvidence{Model: "m", Result: "PASS"}}
	}
	hub := NewHub(HubArgs{StateDir: state, Model: model})
	run, err := hub.Start(context.Background(), StartArgs{Workflow: loadWF(t, reviewWorkflow), Cwd: t.TempDir(), DeferHumanGates: true})
	if err != nil {
		t.Fatal(err)
	}
	res := run.Wait()
	view := run.view()
	reviewable := map[string]bool{}
	for _, s := range view.Steps {
		reviewable[s.ID] = s.Reviewable
	}
	if reviewable["build"] || !reviewable["screen_check"] || !reviewable["looks"] || view.PendingReview != 2 {
		t.Fatalf("reviewable=%v pending=%d", reviewable, view.PendingReview)
	}
	if _, err := hub.Review(run.ID, "build", "pass", ""); err == nil {
		t.Fatal("an automated step accepted an owner review")
	}
	if _, err := hub.Review(run.ID, "looks", "maybe", ""); err == nil || !strings.Contains(err.Error(), "pass, fail, in_game, other") {
		t.Fatalf("unknown finding accepted: %v", err)
	}

	// Typing: notes before a finding are a draft; a finding that requires
	// notes does not settle the step until notes exist.
	if _, err := hub.Review(run.ID, "looks", "", "The car"); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Review(run.ID, "3", "fail", ""); err != nil { // outline number
		t.Fatal(err)
	}
	if v := run.view(); v.PendingReview != 2 || v.Steps[2].Review != "fail" || v.Steps[2].Reviewed {
		t.Fatalf("finding without its required notes settled the step: %+v", v.Steps[2])
	}
	if _, err := hub.Review(run.ID, "looks", "fail", "The car clips\nthrough the wall."); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Review(run.ID, "screen_check", "unclear", ""); err != nil {
		t.Fatal(err)
	}
	// "Check in game" settles the entry for this review without notes and
	// is counted separately as outstanding in-game work.
	if _, err := hub.Review(run.ID, "screen_check", FindingInGame, ""); err != nil {
		t.Fatal(err)
	}
	if v := run.view(); v.PendingReview != 0 || v.NeedsInGame != 1 || !v.Steps[1].Reviewed {
		t.Fatalf("in-game finding: pending=%d in_game=%d step=%+v", v.PendingReview, v.NeedsInGame, v.Steps[1])
	}
	// "Other" needs notes to say what was found.
	if _, err := hub.Review(run.ID, "screen_check", FindingOther, ""); err != nil {
		t.Fatal(err)
	}
	if v := run.view(); v.PendingReview != 1 || v.Steps[1].Reviewed {
		t.Fatalf("Other without notes settled the entry: %+v", v.Steps[1])
	}
	if _, err := hub.Review(run.ID, "screen_check", FindingOther, "Screen was fine but the HUD flickered"); err != nil {
		t.Fatal(err)
	}
	if v := run.view(); v.PendingReview != 0 || !v.Steps[1].Reviewed {
		t.Fatalf("Other with notes: %+v", v.Steps[1])
	}
	if _, err := hub.Review(run.ID, "screen_check", "unclear", ""); err != nil {
		t.Fatal(err)
	}
	if v := run.view(); v.PendingReview != 0 || !v.Steps[2].Reviewed || v.Status != StatusPrepared || res.Status != StatusPrepared {
		t.Fatalf("after reviews: pending=%d step=%+v status=%s", v.PendingReview, v.Steps[2], v.Status)
	}
	if data, err := os.ReadFile(filepath.Join(res.ReportDir, ownerReviewsFile)); err != nil || !strings.Contains(string(data), `through the wall.`) {
		t.Fatalf("bundle export: %v %s", err, data)
	}

	rep, err := hub.Report(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != StatusPrepared || rep.ExitCode != ExitPrepared || rep.StatusBlock == "" || len(rep.Steps) != 3 {
		t.Fatalf("report header: %+v", rep)
	}
	looks := rep.Steps[2]
	if looks.Step.Number != "3" || looks.Prompt != "Does it look right?" || len(looks.Choices) != 2 || len(looks.ReviewOptions) != 4 || looks.ReviewOptions[2].ID != FindingInGame || looks.ReviewOptions[3].ID != FindingOther || looks.Review == nil || looks.Review.Notes != "The car clips\nthrough the wall." {
		t.Fatalf("report step: %+v", looks)
	}
	if a := rep.Steps[1].Appraisal; a == nil || a.Verdict != "accepted" || rep.Steps[1].Review.Finding != "unclear" {
		t.Fatalf("report model step: %+v", rep.Steps[1])
	}
	if sm := rep.Summary; sm.Steps != 3 || sm.Reviewable != 2 || sm.Reviewed != 2 || sm.PendingReview != 0 || sm.OwnerFindings["fail"] != 1 || sm.ByStatus[StepDeferred] != 1 {
		t.Fatalf("report summary: %+v", sm)
	}

	restarted := NewHub(HubArgs{StateDir: state})
	d, err := restarted.Detail(run.ID, "looks")
	if err != nil {
		t.Fatal(err)
	}
	if d.Review == nil || d.Review.Finding != "fail" || d.Review.Notes != "The car clips\nthrough the wall." || len(d.ReviewOptions) != 4 {
		t.Fatalf("review lost across restart: %+v", d)
	}
	if v := restarted.Snapshot().Runs[0]; v.PendingReview != 0 {
		t.Fatalf("restarted pending review = %d", v.PendingReview)
	}
	if _, err := restarted.Review(run.ID, "screen_check", "", ""); err != nil {
		t.Fatal(err)
	}
	if v := restarted.Snapshot().Runs[0]; v.PendingReview != 1 {
		t.Fatalf("clearing a review: pending = %d", v.PendingReview)
	}
}
