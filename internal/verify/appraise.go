// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// defaultAppraiseTimeout bounds one model appraisal of a static gate when
// the workflow sets no appraise.timeout_sec.
const defaultAppraiseTimeout = 180 * time.Second

// appraisalsDir holds one JSON file per appraised step inside the run bundle,
// written as soon as the appraisal exists rather than only in report.json.
const appraisalsDir = "appraisals"

var verdictToken = regexp.MustCompile(`[A-Za-z0-9_.-]+`)

// appraise asks a model for a first-cut verdict on a static gate from a fresh
// screenshot of the gate's device. The verdict never settles the gate; the
// caller records it as deferred for the owner to confirm or override.
func (r *Run) appraise(step Step) *Appraisal {
	started := time.Now()
	a := &Appraisal{}
	timeout := defaultAppraiseTimeout
	if step.AppraiseTimeoutSec != nil {
		timeout = time.Duration(*step.AppraiseTimeoutSec * float64(time.Second))
	}
	if r.hub.model == nil {
		a.Error = "no in-process model executor"
		return a
	}
	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	defer cancel()
	modelStep := Step{
		ID:            step.ID,
		Type:          KindModel,
		Label:         step.Label,
		Device:        step.Device,
		Env:           step.Env,
		ModelSpec:     step.AppraiseModel,
		CaptureScreen: true,
		Prompt:        appraisalPrompt(step),
	}
	r.emit(step.ID, "appraise "+step.ID+"  model reviews the current screen")
	res := r.hub.model(ctx, StepRequest{
		Step:    modelStep,
		Cwd:     r.cwd,
		Env:     r.stepEnv(modelStep),
		Timeout: timeout,
		Emit:    func(line string) { r.emit(step.ID, line) },
	})
	r.scanShots()
	a.DurationMS = time.Since(started).Milliseconds()
	if ev := res.Model; ev != nil {
		a.Provider, a.Model, a.Report, a.Images = ev.Provider, ev.Model, ev.Result, ev.Images
	}
	switch {
	case res.TimedOut:
		a.Error = "model appraisal timed out"
	case res.Code != 0:
		a.Error = strings.TrimSpace(res.Output)
		if a.Error == "" {
			a.Error = fmt.Sprintf("model exit %d", res.Code)
		}
	default:
		if a.Report == "" {
			a.Report = res.Output
		}
		choice := parseVerdict(a.Report, step.Choices)
		if choice == nil {
			a.Error = "model reply did not start with a choice id"
		} else {
			a.Verdict, a.Outcome = choice.ID, choice.Outcome
		}
	}
	return a
}

func appraisalPrompt(step Step) string {
	var b strings.Builder
	b.WriteString("You are giving a first-cut appraisal of an owner verification question, judged from a screenshot of the device's current screen. The owner will later confirm or override your verdict.\n\n")
	fmt.Fprintf(&b, "Question: %s\n", step.Prompt)
	if step.Hint != "" {
		fmt.Fprintf(&b, "Hint: %s\n", step.Hint)
	}
	if step.AppraisePrompt != "" {
		fmt.Fprintf(&b, "\n%s\n", step.AppraisePrompt)
	}
	b.WriteString("\nChoices:\n")
	for _, c := range step.Choices {
		fmt.Fprintf(&b, "- %s: %s\n", c.ID, c.Label)
	}
	b.WriteString("\nReply with exactly one choice id alone on the first line. Then explain, in a few sentences, what in the screenshot supports that choice. If the screenshot cannot settle the question, choose the answer that sends it for investigation and say what is missing.")
	return b.String()
}

// parseVerdict matches the first word of the model's reply to a choice id.
func parseVerdict(report string, choices []Choice) *Choice {
	for line := range strings.SplitSeq(report, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		token := verdictToken.FindString(line)
		for i := range choices {
			if strings.EqualFold(strings.Trim(token, "."), choices[i].ID) {
				return &choices[i]
			}
		}
		return nil
	}
	return nil
}

// modelStepAppraisal keeps a model step's evidence on its record, not only in
// the event log.
func modelStepAppraisal(step Step, res ExecResult, durationMS int64) *Appraisal {
	a := &Appraisal{DurationMS: durationMS}
	if ev := res.Model; ev != nil {
		a.Provider, a.Model, a.Report, a.Images = ev.Provider, ev.Model, ev.Result, ev.Images
	}
	switch {
	case res.Code == 0:
		a.Verdict = "accepted"
		if a.Report == "" {
			a.Report = res.Output
		}
	case a.Report != "" && step.Accept != "":
		a.Verdict = "rejected"
	default:
		a.Error = strings.TrimSpace(res.Output)
	}
	return a
}

func (r *Run) saveAppraisal(rec StepRecord) {
	dir := filepath.Join(r.reportDir, "artifacts", appraisalsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, unsafeName.ReplaceAllString(rec.StepID, "-")+".json"), append(data, '\n'), 0o644)
}

// adoptPriorAppraisals copies a prepared run's model evidence into this run's
// bundle so the owner review is self-contained and its images stay readable
// through this run.
func (r *Run) adoptPriorAppraisals(prior map[string]*Appraisal) {
	if len(prior) == 0 {
		return
	}
	dir := filepath.Join(r.reportDir, "artifacts", "model-inputs")
	adopted := map[string]*Appraisal{}
	for id, a := range prior {
		if a == nil {
			continue
		}
		copied := *a
		copied.Images = nil
		for i, src := range a.Images {
			data, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			name := fmt.Sprintf("prepared-%s-%d%s", unsafeName.ReplaceAllString(id, "-"), i, filepath.Ext(src))
			dst := filepath.Join(dir, name)
			if os.MkdirAll(dir, 0o755) != nil || os.WriteFile(dst, data, 0o644) != nil {
				continue
			}
			copied.Images = append(copied.Images, dst)
		}
		adopted[id] = &copied
	}
	r.mu.Lock()
	r.priorAppraisals = adopted
	r.mu.Unlock()
}

func withoutKey(keys []string, drop string) []string {
	out := keys[:0:0]
	for _, k := range keys {
		if k != drop {
			out = append(out, k)
		}
	}
	return out
}

// StepDetail is one step's full evidence, loaded on demand rather than pushed
// in every snapshot. With no step it carries the run's latest screenshot.
type StepDetail struct {
	RunID      string        `json:"run_id"`
	StepID     string        `json:"step_id,omitempty"`
	Step       *StepView     `json:"step,omitempty"`
	Prompt     string        `json:"prompt,omitempty"`
	Hint       string        `json:"hint,omitempty"`
	Choices    []Choice      `json:"choices,omitempty"`
	Records    []StepRecord  `json:"records,omitempty"`
	Appraisal  *Appraisal    `json:"appraisal,omitempty"`
	Images     []DetailImage `json:"images,omitempty"`
	Screenshot string        `json:"screenshot,omitempty"`
}

// DetailImage is one image a model reviewed, inline for the browser.
type DetailImage struct {
	Path    string `json:"path"`
	DataURI string `json:"data_uri,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Detail returns the evidence for stepID, or the run's latest screenshot when
// stepID is empty. Images are served only from inside this run's bundle.
func (r *Run) Detail(stepID string) (*StepDetail, error) {
	out := &StepDetail{RunID: r.ID, StepID: stepID}
	if stepID == "" {
		out.Screenshot = r.latestShot()
		return out, nil
	}
	view := r.view()
	for i := range view.Steps {
		if view.Steps[i].ID == stepID {
			sv := view.Steps[i]
			out.Step = &sv
		}
	}
	if out.Step == nil {
		return nil, fmt.Errorf("run %s has no step %s", r.ID, stepID)
	}
	for _, s := range append(append([]Step{}, r.wf.Steps...), r.wf.Cleanup...) {
		if s.ID == stepID {
			out.Prompt, out.Hint, out.Choices = s.Prompt, s.Hint, s.Choices
		}
	}
	r.mu.Lock()
	for _, rec := range r.records {
		if rec.StepID == stepID {
			out.Records = append(out.Records, rec)
			if rec.Appraisal != nil {
				out.Appraisal = rec.Appraisal
			}
		}
	}
	if out.Appraisal == nil {
		out.Appraisal = r.priorAppraisals[stepID]
	}
	r.mu.Unlock()
	if out.Appraisal != nil {
		for _, path := range out.Appraisal.Images {
			img := DetailImage{Path: path}
			if rel, err := filepath.Rel(r.reportDir, path); err != nil || !filepath.IsLocal(rel) {
				img.Error = "outside the run bundle"
			} else if img.DataURI = encodeShot(path); img.DataURI == "" {
				img.Error = "unreadable"
			}
			out.Images = append(out.Images, img)
		}
	}
	return out, nil
}
