// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"fmt"
	"maps"
	"time"
)

// Report is a run's full state as one JSON document for agents: run facts,
// the numbered outline, and every step with its definition, records, model
// appraisal and the owner's review. Images are paths inside the run bundle;
// verify_detail returns them inline for one step.
type Report struct {
	RunID          string            `json:"run_id"`
	Workflow       string            `json:"workflow"`
	WorkflowPath   string            `json:"workflow_path,omitempty"`
	Status         string            `json:"status"`
	ExitCode       int               `json:"exit_code"`
	Reason         string            `json:"reason,omitempty"`
	FailedStepID   string            `json:"failed_step_id,omitempty"`
	OwnerComment   string            `json:"owner_comment,omitempty"`
	Owner          string            `json:"owner,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	Unattended     bool              `json:"unattended,omitempty"`
	ReviewDeferred bool              `json:"review_deferred,omitempty"`
	Cwd            string            `json:"cwd"`
	ReportDir      string            `json:"report_dir"`
	Params         map[string]string `json:"params,omitempty"`
	Summary        ReportSummary     `json:"summary"`
	Groups         []Group           `json:"groups,omitempty"`
	Steps          []StepDetail      `json:"steps"`
	StatusBlock    string            `json:"status_block,omitempty"`
}

// ReportSummary counts steps by status and owner review progress.
type ReportSummary struct {
	Steps         int            `json:"steps"`
	ByStatus      map[string]int `json:"by_status"`
	Reviewable    int            `json:"reviewable"`
	Reviewed      int            `json:"reviewed"`
	PendingReview int            `json:"pending_review"`
	ModelVerdicts map[string]int `json:"model_verdicts,omitempty"`
	OwnerFindings map[string]int `json:"owner_findings,omitempty"`
}

// Report returns the full report for a run.
func (h *Hub) Report(runID string) (*Report, error) {
	run := h.RunByID(runID)
	if run == nil {
		return nil, fmt.Errorf("unknown run %s", runID)
	}
	return run.Report(), nil
}

// Report assembles the run's full report from its live or rehydrated state.
func (r *Run) Report() *Report {
	view := r.view()
	out := &Report{
		RunID:          view.RunID,
		Workflow:       view.Workflow,
		WorkflowPath:   r.path,
		Status:         view.Status,
		Owner:          view.Owner,
		StartedAt:      view.StartedAt,
		Unattended:     view.Unattended,
		ReviewDeferred: view.ReviewDeferred,
		Cwd:            view.Cwd,
		ReportDir:      view.ReportDir,
		Groups:         view.Groups,
		Summary: ReportSummary{
			ByStatus:      map[string]int{},
			ModelVerdicts: map[string]int{},
			OwnerFindings: map[string]int{},
		},
	}
	r.mu.Lock()
	out.Params = maps.Clone(r.params)
	if r.status != "running" {
		out.ExitCode = r.exitCode
		out.Reason = r.failedReason
		out.FailedStepID = r.failedID
		out.OwnerComment = r.ownerComment
		out.StatusBlock = r.statusBlock
	}
	r.mu.Unlock()
	for i := range view.Steps {
		sv := view.Steps[i]
		step := StepDetail{RunID: r.ID, StepID: sv.ID, Step: &sv}
		r.fillEvidence(&step)
		out.Steps = append(out.Steps, step)
		out.Summary.Steps++
		out.Summary.ByStatus[sv.Status]++
		if a := step.Appraisal; a != nil && a.Verdict != "" {
			out.Summary.ModelVerdicts[a.Verdict]++
		}
		if sv.Reviewable {
			out.Summary.Reviewable++
			if sv.Reviewed {
				out.Summary.Reviewed++
			}
		}
		if sv.Review != "" {
			out.Summary.OwnerFindings[sv.Review]++
		}
	}
	out.Summary.PendingReview = view.PendingReview
	return out
}
