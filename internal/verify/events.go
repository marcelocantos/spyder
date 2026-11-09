// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

// Event carries lifecycle identity and outcome, never logs, prompts or secrets.
type Event struct {
	Kind, RunID, Workflow, Owner, Device, StepID, Status string
}

func (r *Run) lifecycleEvent(kind, stepID string) {
	if r.hub == nil || r.hub.onEvent == nil {
		return
	}
	r.mu.Lock()
	e := Event{Kind: kind, RunID: r.ID, Workflow: r.wf.Name, Owner: r.owner, Device: r.params["device"], StepID: stepID, Status: r.status}
	r.mu.Unlock()
	if kind == "completed" && e.Status == "running" {
		return
	}
	r.hub.onEvent(e)
}
