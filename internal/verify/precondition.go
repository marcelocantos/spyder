// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// checkPrecondition asks a model whether the gate's device shows the
// expected starting screen, rechecking a few times so transient loading
// screens can clear. It is best effort and never blocks the gate: a "not
// met" verdict becomes a warning, and a model failure leaves it unchecked.
func (r *Run) checkPrecondition(step Step) *PreconditionResult {
	p := step.Precondition
	started := time.Now()
	res := &PreconditionResult{Status: PreconditionUnchecked, Expected: p.Screen}
	defer func() { res.DurationMS = time.Since(started).Milliseconds() }()
	if r.hub.model == nil {
		res.Reason = "no in-process model executor"
		return res
	}
	spec := p.Model
	if spec == nil {
		spec = r.wf.ScreenModel
	}
	if spec == nil {
		spec = json.RawMessage(defaultAppraiseModel)
	}
	// Screenshots and model inputs are named by step ID; keep this check's
	// files apart from the gate's own appraisal.
	check := Step{
		ID:            step.ID + "-precondition",
		Type:          KindModel,
		Label:         step.Label,
		Device:        step.Device,
		Env:           step.Env,
		ModelSpec:     spec,
		CaptureScreen: true,
		// False negatives cost the owner a doubtful review, so judge the
		// screen in substance and fail only a clearly different one.
		Prompt: "Inspect whether this fresh device image is the right starting screen for an owner check. Expected: " + p.Screen +
			"\nJudge it in substance: layout, scroll position, which sub-page or item is showing, and animation state do not matter." +
			" Answer FAIL only when it is clearly a different screen or app, or an error, loading or blank screen." +
			"\nReply with PASS on the first line if it matches, otherwise FAIL, then give a short reason. Do not judge the owner's question itself.",
	}
	release := r.holdMutex(p.Mutex)
	defer release()
	for attempt := 1; attempt <= p.Retry.Count+1; attempt++ {
		if r.ctx.Err() != nil {
			res.Reason = "run stopped before the check finished"
			return res
		}
		res.Attempts = attempt
		ctx, cancel := context.WithTimeout(r.ctx, time.Duration(p.TimeoutSec*float64(time.Second)))
		out := r.hub.model(ctx, StepRequest{Step: check, Cwd: r.cwd, Env: r.stepEnv(check), Timeout: time.Duration(p.TimeoutSec * float64(time.Second)), Emit: func(line string) { r.emit(step.ID, line) }})
		cancel()
		r.scanShots()
		if ev := out.Model; ev != nil {
			res.Provider, res.Model, res.Report, res.Images = ev.Provider, ev.Model, ev.Result, ev.Images
		}
		res.Screenshot = filepath.Join(r.shotsDir(), check.ID+".png")
		if _, err := os.Stat(res.Screenshot); err != nil {
			res.Screenshot = ""
		}
		if out.Code != 0 || res.Report == "" {
			res.Status = PreconditionUnchecked
			res.Reason = strings.TrimSpace(out.Output)
		} else {
			verdict, reason := splitVerdict(res.Report)
			res.Reason = reason
			switch strings.ToUpper(verdict) {
			case "PASS":
				res.Status = PreconditionMet
				return res
			case "FAIL":
				res.Status = PreconditionNotMet
			default:
				res.Status = PreconditionUnchecked
				res.Reason = "model reply did not start with PASS or FAIL"
			}
		}
		if attempt <= p.Retry.Count {
			r.emit(step.ID, fmt.Sprintf("precondition %s  %s; checking again (%d/%d)", step.ID, res.Status, attempt+1, p.Retry.Count+1))
			select {
			case <-r.ctx.Done():
			case <-time.After(time.Duration(p.Retry.BackoffSec * float64(time.Second))):
			}
		}
	}
	return res
}

// splitVerdict returns the first word of a model reply and the rest of it.
func splitVerdict(report string) (string, string) {
	text := strings.TrimSpace(report)
	token := verdictToken.FindString(text)
	rest := strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(text, "*_` "), token), "*_`:.—- \n"))
	return token, oneLine(rest)
}

// holdMutex takes a workflow mutex for the duration of a precondition check,
// so a screen check can wait for builds without the gate holding the mutex
// while the owner looks.
func (r *Run) holdMutex(name string) func() {
	if name == "" {
		return func() {}
	}
	keys := []string{"mutex:" + name}
	for !r.hub.pool.tryStart(r.ID, keys) {
		select {
		case <-r.ctx.Done():
			return func() {}
		case <-time.After(20 * time.Millisecond):
		}
	}
	return func() { r.hub.pool.finish(r.ID, keys) }
}
