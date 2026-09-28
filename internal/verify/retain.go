// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Finished runs stay in the hub for owner review until the agent that
// created them dismisses them. With a state directory, the hub lists every
// retained run bundle in retainedFile and rehydrates them after a restart.
const retainedFile = "retained.json"

// interruptedReason marks a run the daemon stopped before it finished.
const interruptedReason = "daemon stopped before the run finished"

// logTailLines is how much of a rehydrated run's event log the view keeps,
// matching the live run's retained log window.
const logTailLines = 300

type retainedEntry struct {
	RunID     string `json:"run_id"`
	ReportDir string `json:"report_dir"`
}

// saveRetainedLocked writes the index of runs still on the dashboard.
// Callers hold h.mu.
func (h *Hub) saveRetainedLocked() {
	if h.stateDir == "" {
		return
	}
	entries := make([]retainedEntry, 0, len(h.runs))
	for _, r := range h.runs {
		entries = append(entries, retainedEntry{RunID: r.ID, ReportDir: r.reportDir})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(h.stateDir, 0o755); err != nil {
		slog.Warn("verify: retained index", "error", err)
		return
	}
	path := filepath.Join(h.stateDir, retainedFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		slog.Warn("verify: retained index", "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Warn("verify: retained index", "error", err)
	}
}

// rehydrate reloads retained runs from their bundles. A bundle that has gone
// missing drops out of the index; a run interrupted by the restart comes back
// as aborted.
func (h *Hub) rehydrate() {
	data, err := os.ReadFile(filepath.Join(h.stateDir, retainedFile))
	if err != nil {
		return
	}
	var entries []retainedEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("verify: retained index unreadable", "error", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range entries {
		run, err := loadRetained(h, e.ReportDir)
		if err != nil {
			slog.Warn("verify: dropping retained run", "run_id", e.RunID, "report_dir", e.ReportDir, "error", err)
			continue
		}
		h.runs[run.ID] = run
	}
	h.saveRetainedLocked()
}

func loadRetained(h *Hub, dir string) (*Run, error) {
	var meta runMeta
	if err := readJSON(filepath.Join(dir, runMetaFile), &meta); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "workflow.yaml"))
	if err != nil {
		return nil, err
	}
	wf, err := Load(raw)
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	if err := readJSON(filepath.Join(dir, "params.json"), &params); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Run{
		ID:              meta.RunID,
		hub:             h,
		wf:              Substitute(wf, params),
		path:            meta.WorkflowPath,
		cwd:             meta.Cwd,
		params:          params,
		deferHuman:      meta.Unattended,
		reviewDeferred:  meta.ReviewDeferred,
		owner:           meta.Owner,
		startedAt:       meta.StartedAt,
		ctx:             ctx,
		cancel:          cancel,
		stepStatus:      map[string]string{},
		stepDur:         map[string]int64{},
		reportDir:       dir,
		done:            make(chan struct{}),
		appraisals:      map[string]*Appraisal{},
		priorAppraisals: map[string]*Appraisal{},
		reviews:         map[string]*OwnerReview{},
	}
	if saved, err := h.reviews.load(r.ID); err == nil {
		r.reviews = saved
	} else {
		slog.Warn("verify: owner reviews unreadable", "run_id", r.ID, "error", err)
	}
	close(r.done)
	r.logs = tailLines(filepath.Join(dir, "events.log"), logTailLines)

	var res Result
	if err := readJSON(filepath.Join(dir, "report.json"), &res); err != nil {
		res = Result{
			RunID:        r.ID,
			Workflow:     r.wf.Name,
			Device:       params["device"],
			Status:       StatusAborted,
			ExitCode:     ExitAborted,
			FailedReason: interruptedReason,
			ReportDir:    dir,
			Unattended:   r.deferHuman,
			Owner:        r.owner,
		}
		res.StatusBlock = FormatStatus(StatusInput{Status: res.Status, Workflow: res.Workflow, Device: res.Device, FailedReason: res.FailedReason, ReportDir: dir})
		if data, err := json.MarshalIndent(res, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644)
		}
		r.logs = append(r.logs, "aborted "+r.wf.Name+"  "+interruptedReason)
	}
	r.result = res
	r.status, r.exitCode, r.statusBlock = res.Status, res.ExitCode, res.StatusBlock
	r.failedID, r.failedReason, r.ownerComment = res.FailedStepID, res.FailedReason, res.OwnerComment
	r.records = res.Steps
	for _, s := range append(append([]Step{}, r.wf.Steps...), r.wf.Cleanup...) {
		r.stepStatus[s.ID] = StepPending
	}
	for _, rec := range res.Steps {
		r.stepStatus[rec.StepID] = rec.Status
		r.stepDur[rec.StepID] = rec.DurationMS
		if rec.Status == StepDeferred && rec.Appraisal != nil {
			r.appraisals[rec.StepID] = rec.Appraisal
		}
	}
	for id, st := range res.StepStatus {
		r.stepStatus[id] = st
	}
	r.scanShots()
	return r, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > 2*n {
			lines = append([]string{}, lines[len(lines)-n:]...)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// Dismiss removes a finished run from the live view at its creator's request.
// The run bundle stays on disk.
func (h *Hub) Dismiss(runID, owner string) error {
	h.mu.Lock()
	run := h.runs[runID]
	if run == nil {
		h.mu.Unlock()
		return fmt.Errorf("unknown run %s", runID)
	}
	run.mu.Lock()
	status, runOwner := run.status, run.owner
	run.mu.Unlock()
	switch {
	case status == "running":
		h.mu.Unlock()
		return fmt.Errorf("run %s is still active; abort it or wait for it to finish before dismissing", runID)
	case owner != runOwner:
		h.mu.Unlock()
		return fmt.Errorf("run %s was created by %q; only its creator can dismiss it (caller is %q)", runID, runOwner, owner)
	}
	delete(h.runs, runID)
	h.saveRetainedLocked()
	h.mu.Unlock()
	h.notify()
	return nil
}

// runStartOrder sorts runs by start time, so tabs never reorder as runs
// change status.
func runStartOrder(a, b RunView) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.Before(b.StartedAt)
	}
	return a.RunID < b.RunID
}
