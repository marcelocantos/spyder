// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Run is one in-flight (or finished) workflow graph.
type Run struct {
	ID             string
	hub            *Hub
	wf             *Workflow
	path           string
	cwd            string
	params         map[string]string
	answers        map[string]Answer
	allowW         bool
	deferHuman     bool
	reviewDeferred bool
	prior          *Record
	owner          string
	startedAt      time.Time
	ctx            context.Context
	cancel         context.CancelFunc

	mu           sync.Mutex
	okIDs        map[string]bool
	skipIDs      map[string]bool
	passed       map[string]bool
	deferred     map[string]bool
	stepStatus   map[string]string
	stepDur      map[string]int64
	records      []StepRecord
	logs         []string
	logErr       error
	gate         *GateView
	answerCh     chan Answer
	failedID     string
	failedReason string
	ownerComment string
	reportDir    string
	status       string
	exitCode     int
	statusBlock  string
	screenshot   string
	shotKey      string // last encoded path+mod+size, to skip unchanged files
	lastProgress time.Time
	idleExpired  bool
	done         chan struct{}
	result       Result
	// appraisals are this run's model verdicts on deferred static gates;
	// priorAppraisals are a prepared run's verdicts, copied into this bundle
	// for the owner review to confirm or override.
	appraisals      map[string]*Appraisal
	priorAppraisals map[string]*Appraisal
}

// RunOpts is internal construction data.
type RunOpts struct {
	Path            string
	Cwd             string
	Params          map[string]string
	Answers         map[string]Answer
	AllowWaive      bool
	DeferHumanGates bool
	ReviewDeferred  bool
	Prior           *Record
	Owner           string
	Ctx             context.Context
}

func newRun(h *Hub, wf *Workflow, opts RunOpts) *Run {
	parent := opts.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	started := time.Now()
	stamp := started.UTC().Format("20060102T150405Z")
	owner := opts.Owner
	if owner == "" {
		owner = filepath.Base(opts.Cwd)
	}
	report := filepath.Join(opts.Cwd, "verify-runs", stamp+"-"+shortID())
	r := &Run{
		ID:             shortID(),
		hub:            h,
		wf:             wf,
		path:           opts.Path,
		cwd:            opts.Cwd,
		params:         opts.Params,
		answers:        opts.Answers,
		allowW:         opts.AllowWaive,
		deferHuman:     opts.DeferHumanGates,
		reviewDeferred: opts.ReviewDeferred,
		prior:          opts.Prior,
		owner:          owner,
		startedAt:      started,
		ctx:            ctx,
		cancel:         cancel,
		okIDs:          map[string]bool{},
		skipIDs:        map[string]bool{},
		passed:         map[string]bool{},
		deferred:       map[string]bool{},
		stepStatus:     map[string]string{},
		stepDur:        map[string]int64{},
		answerCh:       make(chan Answer, 1),
		reportDir:      report,
		status:         "running",
		lastProgress:   time.Now(),
		done:           make(chan struct{}),
		appraisals:     map[string]*Appraisal{},
	}
	if r.answers == nil {
		r.answers = map[string]Answer{}
	}
	for _, s := range wf.Steps {
		r.stepStatus[s.ID] = StepPending
	}
	return r
}

func (r *Run) persistDefinition() error {
	if err := os.MkdirAll(r.reportDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.reportDir, "workflow.yaml"), r.wf.Raw, 0o644); err != nil {
		return err
	}
	params, err := json.MarshalIndent(r.params, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.reportDir, "params.json"), append(params, '\n'), 0o644); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(runMeta{
		RunID:          r.ID,
		Owner:          r.owner,
		Workflow:       r.wf.Name,
		WorkflowPath:   r.path,
		Cwd:            r.cwd,
		StartedAt:      r.startedAt,
		Unattended:     r.deferHuman,
		ReviewDeferred: r.reviewDeferred,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.reportDir, runMetaFile), append(meta, '\n'), 0o644)
}

// runMetaFile identifies a run bundle: who created it and when. The daemon
// rehydrates retained runs from it after a restart.
const runMetaFile = "run.json"

type runMeta struct {
	RunID          string    `json:"run_id"`
	Owner          string    `json:"owner"`
	Workflow       string    `json:"workflow"`
	WorkflowPath   string    `json:"workflow_path,omitempty"`
	Cwd            string    `json:"cwd"`
	StartedAt      time.Time `json:"started_at"`
	Unattended     bool      `json:"unattended,omitempty"`
	ReviewDeferred bool      `json:"review_deferred,omitempty"`
}

// Wait blocks until the run ends.
func (r *Run) Wait() Result {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

// Result is the JSON body `spyder verify` prints a STATUS block from.
type Result struct {
	RunID        string       `json:"run_id"`
	Workflow     string       `json:"workflow"`
	Device       string       `json:"device,omitempty"`
	Status       string       `json:"status"`
	ExitCode     int          `json:"exit_code"`
	StatusBlock  string       `json:"status_block"`
	FailedStepID string       `json:"failed_step_id,omitempty"`
	FailedReason string       `json:"failed_reason,omitempty"`
	OwnerComment string       `json:"owner_comment,omitempty"`
	ReportDir    string       `json:"report_dir"`
	Steps        []StepRecord `json:"steps"`
	Unattended   bool         `json:"unattended,omitempty"`
	Owner        string       `json:"owner,omitempty"`
	// StepStatus is every step's final state, including resume skips that
	// have no record, so a restarted daemon can redraw the timeline.
	StepStatus map[string]string `json:"step_status,omitempty"`
}

// GateView is the one in-flight owner question.
type GateView struct {
	RunID        string   `json:"run_id"`
	Workflow     string   `json:"workflow"`
	StepID       string   `json:"step_id"`
	Prompt       string   `json:"prompt"`
	Hint         string   `json:"hint,omitempty"`
	Device       string   `json:"device,omitempty"`
	Choices      []Choice `json:"choices"`
	AllowComment bool     `json:"allow_comment"`
	Screenshot   string   `json:"screenshot,omitempty"`
	// Appraisal is a prepared run's model verdict on this gate. The owner
	// confirms it by choosing the same answer, or overrides it.
	Appraisal *Appraisal `json:"appraisal,omitempty"`
}

// RunView is one graph in verify_status. Screenshot is sent only while the
// run is active; a finished run's images load on demand (verify_detail).
type RunView struct {
	RunID          string     `json:"run_id"`
	Workflow       string     `json:"workflow"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason,omitempty"`
	Owner          string     `json:"owner,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	Unattended     bool       `json:"unattended,omitempty"`
	ReviewDeferred bool       `json:"review_deferred,omitempty"`
	PendingReview  int        `json:"pending_review,omitempty"`
	Cwd            string     `json:"cwd"`
	Device         string     `json:"device,omitempty"`
	Groups         []Group    `json:"groups"`
	Steps          []StepView `json:"steps"`
	Log            []string   `json:"log"`
	Gate           *GateView  `json:"gate,omitempty"`
	Screenshot     string     `json:"screenshot,omitempty"`
	ReportDir      string     `json:"report_dir"`
}

// StepView is a dashboard step row. Evaluator says who judged the step:
// "model" for a model step or an appraised gate still awaiting the owner,
// "owner" or "preset" for an answered gate, empty for automation.
type StepView struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Group       string `json:"group,omitempty"`
	Status      string `json:"status"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	ChoiceID    string `json:"choice_id,omitempty"`
	Device      string `json:"device,omitempty"`
	Judgment    string `json:"judgment,omitempty"`
	Evaluator   string `json:"evaluator,omitempty"`
	Appraised   bool   `json:"appraised,omitempty"`
	Verdict     string `json:"verdict,omitempty"`
	ModelReview string `json:"model_review,omitempty"`
}

func (r *Run) view() RunView {
	r.mu.Lock()
	defer r.mu.Unlock()
	steps := make([]StepView, 0, len(r.wf.Steps)+len(r.wf.Cleanup))
	last := map[string]StepRecord{}
	for _, rec := range r.records {
		last[rec.StepID] = rec
	}
	pending := 0
	for _, s := range append(append([]Step{}, r.wf.Steps...), r.wf.Cleanup...) {
		st := r.stepStatus[s.ID]
		if st == "" {
			st = StepPending
		}
		if st == StepDeferred {
			pending++
		}
		label := s.Label
		if label == "" {
			label = s.ID
		}
		sv := StepView{
			ID:         s.ID,
			Label:      label,
			Type:       s.Type,
			Group:      s.Group,
			Status:     st,
			DurationMS: r.stepDur[s.ID],
			Device:     s.Device,
			Judgment:   s.Judgment,
		}
		if rec, ok := last[s.ID]; ok {
			sv.ChoiceID = rec.ChoiceID
			sv.ModelReview = rec.ModelReview
			sv.Evaluator = rec.AnsweredBy
			if a := rec.Appraisal; a != nil {
				sv.Appraised = true
				sv.Verdict = a.Verdict
				if sv.Evaluator == "" {
					sv.Evaluator = "model"
				}
			}
		} else if a := r.priorAppraisals[s.ID]; a != nil {
			sv.Appraised = true
			sv.Verdict = a.Verdict
		}
		steps = append(steps, sv)
	}
	v := RunView{
		RunID:          r.ID,
		Workflow:       r.wf.Name,
		Status:         r.status,
		Owner:          r.owner,
		StartedAt:      r.startedAt,
		Unattended:     r.deferHuman,
		ReviewDeferred: r.reviewDeferred,
		PendingReview:  pending,
		Cwd:            r.cwd,
		Device:         r.params["device"],
		Groups:         r.wf.Groups,
		Steps:          steps,
		Log:            append([]string{}, r.logs...),
		ReportDir:      r.reportDir,
	}
	if r.status == "running" {
		v.Screenshot = r.screenshot
	} else {
		v.Reason = r.failedReason
	}
	if r.gate != nil {
		g := *r.gate
		v.Gate = &g
	}
	return v
}

func (r *Run) poke() {
	if r.hub != nil {
		r.hub.notify()
	}
}

func (r *Run) emit(stepID, line string) {
	r.mu.Lock()
	if r.logErr == nil {
		if err := os.MkdirAll(r.reportDir, 0o755); err != nil {
			r.logErr = err
		} else if file, err := os.OpenFile(filepath.Join(r.reportDir, "events.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			r.logErr = err
		} else {
			_, writeErr := fmt.Fprintln(file, line)
			closeErr := file.Close()
			if writeErr != nil {
				r.logErr = writeErr
			} else if closeErr != nil {
				r.logErr = closeErr
			}
		}
	}
	if len(r.logs) > 400 {
		r.logs = r.logs[len(r.logs)-300:]
	}
	r.logs = append(r.logs, line)
	r.lastProgress = time.Now()
	r.mu.Unlock()
	_ = stepID
	r.poke()
}

func (r *Run) setStatus(id, status string, dur int64) {
	r.mu.Lock()
	r.stepStatus[id] = status
	r.lastProgress = time.Now()
	if dur > 0 {
		r.stepDur[id] = dur
	}
	r.mu.Unlock()
	r.poke()
}

func (r *Run) abort(reason string) {
	r.mu.Lock()
	if reason == "" {
		reason = "aborted by user"
	}
	if r.failedReason == "" {
		r.failedReason = reason
	}
	r.mu.Unlock()
	r.cancel()
	r.hub.pool.broadcast()
	r.poke()
}

func (r *Run) stopOutcome() (string, int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reason := r.failedReason
	if reason == "" {
		reason = "aborted by user"
	}
	if r.idleExpired {
		return StatusInvestigate, ExitInvestigate, reason
	}
	return StatusAborted, ExitAborted, reason
}

func (r *Run) watchIdle() {
	idle := time.Duration(r.wf.IdleTimeoutSec * float64(time.Second))
	tick := idle / 10
	if tick < 10*time.Millisecond {
		tick = 10 * time.Millisecond
	}
	if tick > time.Second {
		tick = time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			elapsed := time.Since(r.lastProgress)
			if elapsed >= idle && r.ctx.Err() == nil {
				r.idleExpired = true
			}
			expired := r.idleExpired
			r.mu.Unlock()
			if expired {
				r.abort(fmt.Sprintf("no progress for %s", idle))
				return
			}
		}
	}
}

func (r *Run) submitAnswer(gateID string, ans Answer) error {
	r.mu.Lock()
	g := r.gate
	r.mu.Unlock()
	if g == nil || g.StepID != gateID {
		return fmt.Errorf("no in-flight gate %s on run %s", gateID, r.ID)
	}
	select {
	case r.answerCh <- ans:
		return nil
	case <-r.ctx.Done():
		return fmt.Errorf("run %s is not waiting for an answer", r.ID)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("gate %s did not accept the answer", gateID)
	}
}

type stepOutcome struct {
	status string // ok, investigate, failed, aborted
	code   int
	reason string
	record StepRecord
}

func (r *Run) drive() {
	defer close(r.done)
	defer r.cancel()
	defer r.hub.notify()
	_ = os.MkdirAll(r.shotsDir(), 0o755)
	go r.watchShots()
	go r.watchIdle()

	prior := r.prior
	if prior == nil {
		prior = LoadRecord(r.cwd, r.wf.Name)
	}
	// A normal run after unattended preparation must restage the whole graph.
	// Reusing its automated passes is reserved for the explicit review mode.
	if prior != nil && !r.reviewDeferred && len(prior.Deferred) > 0 {
		prior = nil
	}
	retry := map[string]bool{}
	if prior != nil {
		if r.reviewDeferred {
			for _, id := range prior.Deferred {
				r.deferred[id] = true
			}
			r.adoptPriorAppraisals(prior.Appraisals)
		} else {
			retry = ResumeRerunIDs(r.wf.Steps, prior.FailedStepID)
		}
		byID := map[string]Step{}
		for _, step := range r.wf.Steps {
			byID[step.ID] = step
		}
		for _, id := range prior.Passed {
			step, known := byID[id]
			if known && r.reviewDeferred && step.Type == KindShell && !step.ReviewReplay {
				r.skipIDs[id] = true
			} else if !r.reviewDeferred && !retry[id] {
				r.skipIDs[id] = true
			}
		}
		failed := prior.FailedStepID
		if failed == "" {
			failed = "next unfinished"
		}
		r.emit("", fmt.Sprintf("resume skip %d passed steps; retry %s", len(r.skipIDs), failed))
	}

	remaining := map[string]bool{}
	byID := map[string]Step{}
	for _, s := range r.wf.Steps {
		byID[s.ID] = s
		remaining[s.ID] = true
	}
	for id := range r.skipIDs {
		delete(remaining, id)
		r.okIDs[id] = true
		r.passed[id] = true
		r.setStatus(id, StepSkipped, 0)
		r.emit(id, "skip "+id+"  already passed")
	}

	type finished struct {
		id  string
		out stepOutcome
	}
	results := make(chan finished, defaultMaxWorkers)
	inflight := 0
	finishRun := func(status string, code int, reason, stepID string) {
		r.cancel()
		for inflight > 0 {
			<-results
			inflight--
		}
		r.finish(status, code, reason, stepID)
	}
	stopRun := func() {
		status, code, reason := r.stopOutcome()
		finishRun(status, code, reason, r.currentFailed())
	}

	r.emit("", "run "+r.wf.Name+" ("+r.ID+")")
	if r.path != "" {
		r.emit("", "workflow "+r.path)
	}
	r.emit("", "report "+r.reportDir)

	for len(remaining) > 0 || inflight > 0 {
		if r.ctx.Err() != nil {
			stopRun()
			return
		}

		for _, s := range r.wf.Steps {
			if !remaining[s.ID] {
				continue
			}
			missing := false
			for _, req := range s.Requires {
				if !r.okIDs[req] {
					missing = true
					break
				}
			}
			if missing {
				continue
			}
			keys := resourceKeys(s)
			if r.deferHuman && s.Type == KindHumanGate {
				// No owner is asked, so the daemon-wide owner lock stays free
				// for attended runs while a model appraises this gate.
				keys = withoutKey(keys, resourceKeyHuman)
			}
			if !r.hub.pool.tryStart(r.ID, keys) {
				continue
			}
			delete(remaining, s.ID)
			label := s.Label
			r.setStatus(s.ID, StepRunning, 0)
			if s.Type == KindHumanGate && !r.deferHuman {
				r.setStatus(s.ID, StepWaiting, 0)
			}
			msg := "--> " + s.ID + "  " + s.Type
			if label != "" {
				msg += "  " + label
			}
			r.emit(s.ID, msg)
			step := s
			inflight++
			go func() {
				defer r.hub.pool.finish(r.ID, keys)
				results <- finished{id: step.ID, out: r.runOne(step)}
			}()
		}

		if inflight == 0 {
			if len(remaining) == 0 {
				break
			}
			blockedOnPool := false
			for _, s := range r.wf.Steps {
				if !remaining[s.ID] {
					continue
				}
				ready := true
				for _, req := range s.Requires {
					if !r.okIDs[req] {
						ready = false
						break
					}
				}
				if ready {
					blockedOnPool = true
					break
				}
			}
			if !blockedOnPool {
				var blocked []string
				for _, s := range r.wf.Steps {
					if remaining[s.ID] {
						blocked = append(blocked, s.ID)
					}
				}
				finishRun(StatusFailed, ExitError, "requires not satisfied: "+join(blocked), blocked[0])
				return
			}
			select {
			case <-r.ctx.Done():
				stopRun()
				return
			case <-time.After(20 * time.Millisecond):
			}
			continue
		}

		poolBlocked := false
		for _, s := range r.wf.Steps {
			if !remaining[s.ID] {
				continue
			}
			ready := true
			for _, req := range s.Requires {
				if !r.okIDs[req] {
					ready = false
					break
				}
			}
			if ready {
				poolBlocked = true
				break
			}
		}

		var got finished
		if poolBlocked {
			select {
			case got = <-results:
			case <-r.ctx.Done():
				stopRun()
				return
			case <-time.After(20 * time.Millisecond):
				continue
			}
		} else {
			select {
			case got = <-results:
			case <-r.ctx.Done():
				stopRun()
				return
			}
		}
		inflight--
		if r.ctx.Err() != nil {
			stopRun()
			return
		}
		if got.out.status != "ok" && got.out.status != StepDeferred {
			finishRun(got.out.status, got.out.code, got.out.reason, got.id)
			return
		}
		r.okIDs[got.id] = true
		if got.out.status == StepDeferred {
			r.deferred[got.id] = true
		} else {
			delete(r.deferred, got.id)
			r.rememberPass(got.id)
		}
	}

	if len(r.deferred) > 0 {
		finishRun(StatusPrepared, ExitPrepared, "owner judgments deferred", "")
		return
	}
	finishRun(StatusPassed, ExitPassed, "", "")
}

func (r *Run) currentFailed() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failedID
}

func (r *Run) runOne(step Step) stepOutcome {
	if step.Type == KindHumanGate {
		if r.deferHuman {
			rec := StepRecord{StepID: step.ID, Status: StepDeferred, Type: KindHumanGate, Label: step.Label, Judgment: step.Judgment}
			if step.Judgment == JudgmentStatic {
				rec.Appraisal = r.appraise(step)
				rec.DurationMS = rec.Appraisal.DurationMS
				r.mu.Lock()
				r.appraisals[step.ID] = rec.Appraisal
				r.mu.Unlock()
			}
			r.record(rec)
			r.setStatus(step.ID, StepDeferred, rec.DurationMS)
			switch a := rec.Appraisal; {
			case a == nil:
				r.emit(step.ID, "deferred "+step.ID+"  owner review pending")
			case a.Verdict != "":
				r.emit(step.ID, "deferred "+step.ID+"  model verdict "+a.Verdict+"; owner confirmation pending")
			default:
				r.emit(step.ID, "deferred "+step.ID+"  model appraisal failed ("+oneLine(a.Error)+"); owner review pending")
			}
			return stepOutcome{status: StepDeferred}
		}
		return r.humanGate(step)
	}
	return r.commandStep(step)
}

func (r *Run) commandStep(step Step) stepOutcome {
	retry := step.Retry
	attempts := 1
	backoff := time.Duration(0)
	if retry != nil {
		attempts = retry.Count + 1
		backoff = time.Duration(retry.BackoffSec * float64(time.Second))
	}
	var last ExecResult
	var started time.Time
	for attempt := 1; attempt <= attempts; attempt++ {
		started = time.Now()
		last = r.execStep(step)
		r.scanShots()
		dur := time.Since(started).Milliseconds()
		st := StepFailed
		if last.Code == 0 {
			st = StepOK
		}
		rec := StepRecord{
			StepID:     step.ID,
			Status:     st,
			DurationMS: dur,
			Type:       step.Type,
			Label:      step.Label,
		}
		if step.Type == KindModel {
			rec.Appraisal = modelStepAppraisal(step, last, dur)
		}
		if last.Code == 0 {
			r.record(rec)
			r.setStatus(step.ID, StepOK, dur)
			r.emit(step.ID, fmt.Sprintf("ok %s  %dms", step.ID, dur))
			return stepOutcome{status: "ok", code: 0, record: rec}
		}
		if attempt < attempts {
			r.emit(step.ID, fmt.Sprintf("retry %s  %d/%d in %s", step.ID, attempt+1, attempts, backoff))
			r.hub.sleep(backoff)
		} else {
			if step.Type == KindModel && last.Output != "" {
				r.emit(step.ID, last.Output)
			}
			r.record(rec)
			r.setStatus(step.ID, StepFailed, dur)
		}
	}
	reason := fmt.Sprintf("%s: exit %d", step.Type, last.Code)
	if last.TimedOut {
		reason = step.Type + ": timeout"
	}
	r.emit(step.ID, "investigate "+step.ID+"  "+reason)
	return stepOutcome{status: StatusInvestigate, code: ExitInvestigate, reason: reason}
}

func (r *Run) stepEnv(step Step) map[string]string {
	env := map[string]string{
		"SPYDER_VERIFY_REPORT_DIR":   r.reportDir,
		"SPYDER_VERIFY_ARTIFACT_DIR": filepath.Join(r.reportDir, "artifacts"),
		"SPYDER_VERIFY_WORKFLOW":     r.wf.Name,
		"SPYDER_VERIFY_STEP_ID":      step.ID,
		"SPYDER_RUN_DIR":             r.shotsDir(),
	}
	for k, v := range step.Env {
		env[k] = v
	}
	return env
}

func (r *Run) execStep(step Step) ExecResult {
	env := r.stepEnv(step)
	timeout := time.Duration(0)
	if step.TimeoutSec != nil {
		timeout = time.Duration(*step.TimeoutSec * float64(time.Second))
	}
	ctx := r.ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(r.ctx, timeout)
		defer cancel()
	}
	emit := func(line string) { r.emit(step.ID, line) }
	req := StepRequest{Step: step, Cwd: r.cwd, Env: env, Timeout: timeout, Emit: emit}
	if step.Type == KindSpyderScript {
		if r.hub.script == nil {
			return ExecResult{Code: 1, Output: "spyder_script: no in-process executor"}
		}
		return r.hub.script(ctx, req)
	}
	if step.Type == KindModel {
		if r.hub.model == nil {
			return ExecResult{Code: 1, Output: "model: no in-process executor"}
		}
		return r.hub.model(ctx, req)
	}
	return r.hub.shell(ctx, req)
}

func (r *Run) humanGate(step Step) stepOutcome {
	started := time.Now()
	view := &GateView{
		RunID:        r.ID,
		Workflow:     r.wf.Name,
		StepID:       step.ID,
		Prompt:       step.Prompt,
		Hint:         step.Hint,
		Device:       step.Device,
		Choices:      step.Choices,
		AllowComment: step.AllowComment,
		Screenshot:   r.latestShot(),
	}
	if view.Device == "" {
		view.Device = r.params["device"]
	}
	r.mu.Lock()
	prior := r.priorAppraisals[step.ID]
	r.mu.Unlock()
	if prior != nil {
		a := *prior
		view.Appraisal = &a
	}
	r.mu.Lock()
	r.gate = view
	r.mu.Unlock()
	r.poke()
	defer func() {
		r.mu.Lock()
		r.gate = nil
		r.mu.Unlock()
		r.poke()
	}()

	var ans Answer
	answeredBy := AnsweredByOwner
	if preset, ok := r.answers[step.ID]; ok {
		ans = preset
		answeredBy = AnsweredByPreset
	} else {
		var timeout <-chan time.Time
		if step.TimeoutSec != nil {
			t := time.NewTimer(time.Duration(*step.TimeoutSec * float64(time.Second)))
			defer t.Stop()
			timeout = t.C
		}
		select {
		case ans = <-r.answerCh:
		case <-timeout:
			dur := time.Since(started).Milliseconds()
			r.record(StepRecord{StepID: step.ID, Status: StepFailed, DurationMS: dur, Type: KindHumanGate, Label: step.Label})
			r.setStatus(step.ID, StepFailed, dur)
			r.emit(step.ID, "investigate "+step.ID+"  human_gate: timeout")
			return stepOutcome{status: StatusInvestigate, code: ExitInvestigate, reason: "human_gate: timeout"}
		case <-r.ctx.Done():
			dur := time.Since(started).Milliseconds()
			r.record(StepRecord{StepID: step.ID, Status: StepAborted, DurationMS: dur, Type: KindHumanGate, Label: step.Label})
			return stepOutcome{status: StatusAborted, code: ExitAborted, reason: "aborted by user"}
		}
	}

	var choice *Choice
	for i := range step.Choices {
		if step.Choices[i].ID == ans.ChoiceID {
			choice = &step.Choices[i]
			break
		}
	}
	dur := time.Since(started).Milliseconds()
	if choice == nil {
		r.record(StepRecord{StepID: step.ID, Status: StepFailed, DurationMS: dur, Type: KindHumanGate, ChoiceID: ans.ChoiceID})
		r.setStatus(step.ID, StepFailed, dur)
		return stepOutcome{status: StatusFailed, code: ExitError, reason: "human_gate: unknown choice " + ans.ChoiceID}
	}
	if choice.Outcome == OutcomeWaive && !r.allowW {
		r.record(StepRecord{StepID: step.ID, Status: StepFailed, DurationMS: dur, Type: KindHumanGate, ChoiceID: choice.ID})
		return stepOutcome{status: StatusFailed, code: ExitError, reason: "human_gate: waive requires --allow-waive"}
	}
	if choice.Comment == "required" && stringsTrim(ans.Comment) == "" {
		r.record(StepRecord{StepID: step.ID, Status: StepFailed, DurationMS: dur, Type: KindHumanGate, ChoiceID: choice.ID})
		return stepOutcome{status: StatusFailed, code: ExitError, reason: "human_gate: comment required"}
	}
	r.mu.Lock()
	r.ownerComment = ans.Comment
	r.mu.Unlock()
	rec := StepRecord{
		StepID:     step.ID,
		Status:     StepOK,
		DurationMS: dur,
		ChoiceID:   choice.ID,
		Comment:    ans.Comment,
		Type:       KindHumanGate,
		Label:      step.Label,
		Judgment:   step.Judgment,
		AnsweredBy: answeredBy,
	}
	if prior != nil {
		rec.Appraisal = prior
		rec.ModelReview = ModelOverridden
		if prior.Verdict == choice.ID {
			rec.ModelReview = ModelConfirmed
		}
	}
	r.record(rec)
	if choice.Outcome == OutcomeInvestigate {
		reason := "human_gate: choice=" + choice.ID
		r.setStatus(step.ID, StepFailed, dur)
		r.emit(step.ID, fmt.Sprintf("investigate %s  %s  %dms", step.ID, reason, dur))
		return stepOutcome{status: StatusInvestigate, code: ExitInvestigate, reason: reason, record: rec}
	}
	r.setStatus(step.ID, StepOK, dur)
	r.emit(step.ID, fmt.Sprintf("ok %s  choice=%s  %dms", step.ID, choice.ID, dur))
	return stepOutcome{status: "ok", code: 0, record: rec}
}

func (r *Run) record(rec StepRecord) {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
	if rec.Appraisal != nil {
		r.saveAppraisal(rec)
	}
}

func (r *Run) rememberPass(id string) {
	r.mu.Lock()
	r.passed[id] = true
	passed := keys(r.passed)
	r.mu.Unlock()
	_ = SaveRecord(r.cwd, r.wf.Name, &Record{
		Workflow: r.wf.Name,
		Passed:   passed,
		Params:   r.params,
	})
}

func (r *Run) latestShot() string {
	r.scanShots()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.screenshot
}

const maxShotBytes = 8 << 20 // 8 MiB; live phone frames stay well under this

func (r *Run) shotsDir() string {
	return filepath.Join(r.reportDir, "artifacts", "screenshots")
}

func (r *Run) watchShots() {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			r.scanShots()
			return
		case <-ticker.C:
			r.scanShots()
		}
	}
}

func (r *Run) scanShots() {
	dir := r.shotsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var best string
	var bestMod time.Time
	var bestSize int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestMod) || best == "" {
			best = filepath.Join(dir, e.Name())
			bestMod = info.ModTime()
			bestSize = info.Size()
		}
	}
	if best == "" {
		return
	}
	key := fmt.Sprintf("%s:%d:%d", best, bestMod.UnixNano(), bestSize)
	r.mu.Lock()
	same := r.shotKey == key
	r.mu.Unlock()
	if same {
		return
	}
	uri := encodeShot(best)
	if uri == "" {
		return
	}
	r.mu.Lock()
	r.shotKey = key
	r.screenshot = uri
	if r.gate != nil {
		r.gate.Screenshot = uri
	}
	r.mu.Unlock()
	r.poke()
}

func encodeShot(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || int64(len(b)) > maxShotBytes {
		return ""
	}
	mime := "image/png"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

// runCleanup uses fresh contexts because failure and abort cancel the run
// context. Every cleanup command is attempted, even if an earlier one fails.
func (r *Run) runCleanup() (string, string) {
	var firstID string
	var failures []string
	for _, step := range r.wf.Cleanup {
		r.setStatus(step.ID, StepRunning, 0)
		r.emit(step.ID, "--> "+step.ID+"  cleanup")
		timeout := 30 * time.Second
		if step.TimeoutSec != nil {
			timeout = time.Duration(*step.TimeoutSec * float64(time.Second))
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		env := map[string]string{
			"SPYDER_VERIFY_REPORT_DIR": r.reportDir,
			"SPYDER_VERIFY_WORKFLOW":   r.wf.Name,
			"SPYDER_VERIFY_STEP_ID":    step.ID,
		}
		for k, v := range step.Env {
			env[k] = v
		}
		started := time.Now()
		keys := resourceKeys(step)
		acquired := false
		for !acquired && ctx.Err() == nil {
			acquired = r.hub.pool.tryStart(r.ID, keys)
			if !acquired {
				select {
				case <-ctx.Done():
				case <-time.After(20 * time.Millisecond):
				}
			}
		}
		out := ExecResult{Code: 124, TimedOut: true}
		if acquired {
			out = r.hub.shell(ctx, StepRequest{
				Step: step, Cwd: r.cwd, Env: env, Timeout: timeout,
				Emit: func(line string) { r.emit(step.ID, line) },
			})
			r.hub.pool.finish(r.ID, keys)
		}
		cancel()
		dur := time.Since(started).Milliseconds()
		stepStatus := StepOK
		if out.Code != 0 {
			stepStatus = StepFailed
			if firstID == "" {
				firstID = step.ID
			}
			failures = append(failures, fmt.Sprintf("%s exit %d", step.ID, out.Code))
		}
		r.record(StepRecord{StepID: step.ID, Status: stepStatus, DurationMS: dur, Type: KindShell, Label: step.Label})
		r.setStatus(step.ID, stepStatus, dur)
		r.emit(step.ID, fmt.Sprintf("%s %s  %dms", stepStatus, step.ID, dur))
	}
	if len(failures) > 0 {
		return firstID, "cleanup: " + strings.Join(failures, ", ")
	}
	return "", ""
}

func (r *Run) finish(status string, code int, reason, stepID string) {
	r.cancel()
	cleanupID, cleanupReason := r.runCleanup()
	if cleanupID != "" {
		if status == StatusPassed || status == StatusPrepared {
			status, code, stepID, reason = StatusInvestigate, ExitInvestigate, cleanupID, cleanupReason
		} else if reason == "" {
			reason = cleanupReason
		} else {
			reason += "; " + cleanupReason
		}
	}
	r.mu.Lock()
	logErr := r.logErr
	r.mu.Unlock()
	if logErr != nil {
		status, code = StatusFailed, ExitError
		reason = "event log: " + logErr.Error()
	}
	r.mu.Lock()
	r.status = status
	r.exitCode = code
	if status != StatusPassed {
		if stepID != "" {
			r.failedID = stepID
		}
		if reason != "" {
			r.failedReason = reason
		}
	} else {
		r.failedID = ""
		r.failedReason = ""
	}
	passed := keys(r.passed)
	if status == StatusPassed {
		passed = keys(r.okIDs)
	}
	failed := r.failedID
	block := FormatStatus(StatusInput{
		Status:       status,
		Workflow:     r.wf.Name,
		Device:       r.params["device"],
		FailedStepID: r.failedID,
		FailedReason: r.failedReason,
		SkipN:        len(r.skipIDs),
		Steps:        append([]StepRecord{}, r.records...),
		OwnerComment: r.ownerComment,
		ReportDir:    r.reportDir,
	})
	r.statusBlock = block
	r.result = Result{
		RunID:        r.ID,
		Workflow:     r.wf.Name,
		Device:       r.params["device"],
		Status:       status,
		ExitCode:     code,
		StatusBlock:  block,
		FailedStepID: r.failedID,
		FailedReason: r.failedReason,
		OwnerComment: r.ownerComment,
		ReportDir:    r.reportDir,
		Steps:        append([]StepRecord{}, r.records...),
		Unattended:   r.deferHuman,
		Owner:        r.owner,
		StepStatus:   maps.Clone(r.stepStatus),
	}
	appraisals := map[string]*Appraisal{}
	for id := range r.deferred {
		if a := r.appraisals[id]; a != nil {
			appraisals[id] = a
		} else if a := r.priorAppraisals[id]; a != nil {
			appraisals[id] = a
		}
	}
	r.mu.Unlock()
	data, reportErr := json.MarshalIndent(r.result, "", "  ")
	if reportErr == nil {
		data = append(data, '\n')
		reportErr = os.WriteFile(filepath.Join(r.reportDir, "report.json"), data, 0o644)
	}
	if reportErr != nil {
		status = StatusFailed
		reason = "report: " + reportErr.Error()
		r.mu.Lock()
		r.status = status
		r.exitCode = ExitError
		r.failedReason = reason
		r.statusBlock = FormatStatus(StatusInput{
			Status:       status,
			Workflow:     r.wf.Name,
			Device:       r.params["device"],
			FailedStepID: r.failedID,
			FailedReason: reason,
			SkipN:        len(r.skipIDs),
			Steps:        append([]StepRecord{}, r.records...),
			OwnerComment: r.ownerComment,
			ReportDir:    r.reportDir,
		})
		r.result.Status = status
		r.result.ExitCode = ExitError
		r.result.FailedReason = reason
		r.result.StatusBlock = r.statusBlock
		block = r.statusBlock
		r.mu.Unlock()
	}

	rec := &Record{Workflow: r.wf.Name, Passed: passed, Deferred: keys(r.deferred), DefinitionSHA256: fmt.Sprintf("%x", sha256.Sum256(r.wf.Raw)), Params: r.params}
	if len(appraisals) > 0 {
		rec.Appraisals = appraisals
	}
	if status != StatusPassed {
		rec.FailedStepID = failed
	}
	_ = SaveRecord(r.cwd, r.wf.Name, rec)

	if reason != "" {
		r.emit("", status+" "+r.wf.Name+"  "+reason)
	} else {
		r.emit("", status+" "+r.wf.Name)
	}
	for _, line := range splitLines(block) {
		r.emit("", line)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func stringsTrim(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}
