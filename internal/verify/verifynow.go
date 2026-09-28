// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"fmt"
)

// StagingChain returns the staging scripts that put the app into the state
// an entry was reviewed in. It walks back through requires, passing through
// model checks and review_replay shell checks without keeping them (Verify
// Now reassesses nothing), and stops at owner gates and at other shell steps
// (builds and deploys), which count as done.
func StagingChain(wf *Workflow, target string) map[string]bool {
	byID := map[string]Step{}
	for _, s := range wf.Steps {
		byID[s.ID] = s
	}
	chain := map[string]bool{}
	seen := map[string]bool{target: true}
	stack := []string{target}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, req := range byID[id].Requires {
			s, ok := byID[req]
			if !ok || seen[req] {
				continue
			}
			seen[req] = true
			switch {
			case s.Type == KindSpyderScript:
				chain[req] = true
				stack = append(stack, req)
			case s.Type == KindModel || (s.Type == KindShell && s.ReviewReplay):
				stack = append(stack, req)
			}
		}
	}
	return chain
}

// VerifyNow puts the app back into the state an entry awaiting review was
// judged in, and leaves it there for the owner. It runs only the entry's
// staging scripts from the run's saved workflow and parameters: no builds,
// checks, gates or cleanup. The owner records their finding in the entry's
// review as usual. Best effort: if staging fails, the owner checks by hand.
// A new request for the same entry replaces the previous one.
func (h *Hub) VerifyNow(runID, stepRef string) (*Run, error) {
	source := h.RunByID(runID)
	if source == nil {
		return nil, fmt.Errorf("unknown run %s", runID)
	}
	step, ok := source.stepByRef(stepRef)
	if !ok {
		return nil, fmt.Errorf("run %s has no step %s", runID, stepRef)
	}
	source.mu.Lock()
	status := source.status
	options := reviewOptions(step, source.stepStatus[step.ID])
	raw, params := source.wf.Raw, source.params
	source.mu.Unlock()
	switch {
	case status == "running":
		return nil, fmt.Errorf("run %s is still active", runID)
	case options == nil:
		return nil, fmt.Errorf("step %s is not awaiting owner review", step.ID)
	case len(StagingChain(source.wf, step.ID)) == 0:
		return nil, fmt.Errorf("step %s has no staging script to replay; check it by hand", step.ID)
	}
	wf, err := Load(raw)
	if err != nil {
		return nil, fmt.Errorf("saved workflow: %w", err)
	}
	h.dismissFocusRuns(source.ID, step.ID, false)
	return h.Start(context.Background(), StartArgs{
		Workflow:     wf,
		WorkflowPath: source.path,
		Cwd:          source.cwd,
		Params:       params,
		Owner:        source.owner,
		Focus:        step.ID,
		FocusSource:  source.ID,
	})
}

// dismissFocusRuns drops finished Verify Now runs of a source run (for one
// step, or all when stepID is empty). With abort, active ones are stopped
// first; otherwise they are left to finish.
func (h *Hub) dismissFocusRuns(sourceID, stepID string, abort bool) {
	h.mu.Lock()
	var active []*Run
	for id, r := range h.runs {
		if r.focusSource != sourceID || (stepID != "" && r.focus != stepID) {
			continue
		}
		r.mu.Lock()
		running := r.status == "running"
		r.mu.Unlock()
		if running {
			active = append(active, r)
			continue
		}
		delete(h.runs, id)
	}
	h.saveRetainedLocked()
	h.mu.Unlock()
	for _, r := range active {
		if !abort {
			continue
		}
		r.abort("owner moved on")
		go func(r *Run) {
			r.Wait()
			h.mu.Lock()
			delete(h.runs, r.ID)
			h.saveRetainedLocked()
			h.mu.Unlock()
			h.notify()
		}(r)
	}
	h.notify()
}
