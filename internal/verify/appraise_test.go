// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const appraiseWorkflow = `
name: appraise-sample
params:
  device: handset
steps:
  - id: stage
    type: spyder_script
    script: stage.star
  - id: look_ok
    type: human_gate
    requires: [stage]
    judgment: static
    device: ${device}
    prompt: Is the garage shown on ${device}?
    hint: The garage has a red door.
    appraise:
      prompt: Ignore the status bar.
    choices:
      - id: pass
        label: Yes
      - id: fail
        label: No
        outcome: investigate
  - id: look_bad
    type: human_gate
    requires: [look_ok]
    judgment: static
    device: ${device}
    prompt: Is the shop shown?
    choices:
      - id: pass
        label: Yes
      - id: fail
        label: No
        outcome: investigate
  - id: play
    type: human_gate
    requires: [look_bad]
    judgment: dynamic
    prompt: Does the car handle well?
    choices:
      - id: pass
        label: Yes
  - id: broken
    type: human_gate
    requires: [play]
    judgment: static
    device: ${device}
    prompt: Is the menu shown?
    choices:
      - id: pass
        label: Yes
  - id: after
    type: shell
    requires: [broken]
    argv: [/usr/bin/true]
`

// fakeScreenModel mimics the daemon's model runner: it writes the image it
// "reviews" into the run bundle and reports it as evidence.
func fakeScreenModel(t *testing.T, replies map[string]string, prompts *sync.Map) StepRunner {
	return func(_ context.Context, req StepRequest) ExecResult {
		if prompts != nil {
			prompts.Store(req.Step.ID, req.Step.Prompt)
		}
		if !req.Step.CaptureScreen || req.Step.Device == "" {
			t.Errorf("%s: appraisal must capture the device screen: %+v", req.Step.ID, req.Step)
		}
		shots := req.Env["SPYDER_RUN_DIR"]
		image := filepath.Join(filepath.Dir(shots), "model-inputs", req.Step.ID+".jpg")
		_ = os.MkdirAll(filepath.Dir(image), 0o755)
		_ = os.WriteFile(image, []byte("\xff\xd8\xff\xe0 fake jpeg "+req.Step.ID), 0o644)
		ev := &ModelEvidence{Provider: "claude", Model: "claude-test", Images: []string{image}}
		reply, ok := replies[req.Step.ID]
		if !ok {
			return ExecResult{Code: 1, Output: "claudia task: no final result", Model: ev}
		}
		ev.Result = reply
		return ExecResult{Code: 0, Output: reply, Model: ev}
	}
}

func TestValidateGateJudgment(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"static needs device", "name: x\nsteps:\n  - id: g\n    type: human_gate\n    judgment: static\n    prompt: P\n    choices: [{id: pass, label: Yes}]\n", "judgment static requires device"},
		{"appraise needs static", "name: x\nsteps:\n  - id: g\n    type: human_gate\n    device: d\n    appraise: {prompt: x}\n    prompt: P\n    choices: [{id: pass, label: Yes}]\n", "appraise requires judgment: static"},
		{"unknown judgment", "name: x\nsteps:\n  - id: g\n    type: human_gate\n    judgment: vibes\n    prompt: P\n    choices: [{id: pass, label: Yes}]\n", "judgment must be static or dynamic"},
		{"bad appraise model", "name: x\nsteps:\n  - id: g\n    type: human_gate\n    judgment: static\n    device: d\n    appraise: {model: {colour: blue}}\n    prompt: P\n    choices: [{id: pass, label: Yes}]\n", "unknown Claudia model predicate colour"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
	wf := loadWF(t, appraiseWorkflow)
	for _, s := range wf.Steps {
		if s.ID == "look_bad" && (s.Judgment != JudgmentStatic || !strings.Contains(string(s.AppraiseModel), "claude")) {
			t.Fatalf("static gate without appraise.model has no default model: %+v", s)
		}
		if s.ID == "play" && s.Judgment != JudgmentDynamic {
			t.Fatalf("dynamic gate: %+v", s)
		}
	}
	if loadWF(t, "name: x\nsteps:\n  - id: g\n    type: human_gate\n    prompt: P\n    choices: [{id: pass, label: Yes}]\n").Steps[0].Judgment != JudgmentDynamic {
		t.Fatal("an undeclared judgment must default to dynamic, which is never appraised")
	}
}

// 🎯T149.2: unattended runs appraise static gates with a model, defer dynamic
// ones, never record a model verdict as an owner pass, and the owner review
// shows each verdict for confirmation or override.
func TestUnattendedAppraisesStaticGatesForOwnerReview(t *testing.T) {
	var prompts sync.Map
	hub := NewHub(HubArgs{
		Script: func(context.Context, StepRequest) ExecResult { return ExecResult{} },
		Model: fakeScreenModel(t, map[string]string{
			"look_ok":  "PASS\nThe red garage door fills the screen.",
			"look_bad": "**fail**\nThis is the settings menu, not the shop.",
		}, &prompts),
	})
	cwd := t.TempDir()
	wf := loadWF(t, appraiseWorkflow)
	run, err := hub.Start(context.Background(), StartArgs{Workflow: wf, Cwd: cwd, DeferHumanGates: true})
	if err != nil {
		t.Fatal(err)
	}
	res := run.Wait()
	if res.Status != StatusPrepared {
		t.Fatalf("status=%s reason=%s\n%s", res.Status, res.FailedReason, res.StatusBlock)
	}
	byID := map[string]StepRecord{}
	for _, rec := range res.Steps {
		byID[rec.StepID] = rec
	}
	for id, want := range map[string]struct{ verdict, outcome, errText string }{
		"look_ok":  {"pass", OutcomeContinue, ""},
		"look_bad": {"fail", OutcomeInvestigate, ""},
		"broken":   {"", "", "no final result"},
	} {
		rec := byID[id]
		a := rec.Appraisal
		if rec.Status != StepDeferred || rec.ChoiceID != "" || rec.AnsweredBy != "" || a == nil {
			t.Fatalf("%s must stay deferred with a model appraisal and no owner answer: %+v", id, rec)
		}
		if a.Verdict != want.verdict || a.Outcome != want.outcome || !strings.Contains(a.Error, want.errText) || a.Model != "claude-test" || len(a.Images) != 1 {
			t.Fatalf("%s appraisal: %+v", id, a)
		}
		if rel, err := filepath.Rel(res.ReportDir, a.Images[0]); err != nil || !filepath.IsLocal(rel) {
			t.Fatalf("%s image outside the bundle: %s", id, a.Images[0])
		}
		if _, err := os.Stat(filepath.Join(res.ReportDir, "artifacts", appraisalsDir, id+".json")); err != nil {
			t.Fatalf("%s appraisal not saved in the bundle: %v", id, err)
		}
	}
	if byID["look_ok"].Appraisal.Report != "PASS\nThe red garage door fills the screen." {
		t.Fatalf("full model report not kept: %q", byID["look_ok"].Appraisal.Report)
	}
	if rec := byID["play"]; rec.Status != StepDeferred || rec.Appraisal != nil || rec.Judgment != JudgmentDynamic {
		t.Fatalf("dynamic gate must be deferred without a model: %+v", rec)
	}
	if p, _ := prompts.Load("look_ok"); !strings.Contains(p.(string), "Is the garage shown on handset?") || !strings.Contains(p.(string), "- fail: No") || !strings.Contains(p.(string), "Ignore the status bar.") || !strings.Contains(p.(string), "red door") {
		t.Fatalf("appraisal prompt: %q", p)
	}
	if _, asked := prompts.Load("play"); asked {
		t.Fatal("a dynamic gate was sent to the model")
	}
	if !strings.Contains(res.StatusBlock, "look_ok  deferred") || !strings.Contains(res.StatusBlock, "model=pass") || !strings.Contains(res.StatusBlock, "model=fail") {
		t.Fatalf("STATUS block hides model verdicts:\n%s", res.StatusBlock)
	}
	rec := LoadRecord(cwd, wf.Name)
	if rec == nil || len(rec.Deferred) != 4 || len(rec.Appraisals) != 3 || rec.Appraisals["look_bad"].Verdict != "fail" {
		t.Fatalf("resume record: %+v", rec)
	}
	for _, id := range rec.Passed {
		if strings.HasPrefix(id, "look") || id == "play" || id == "broken" {
			t.Fatalf("a deferred gate was recorded as passed: %v", rec.Passed)
		}
	}
	view := run.view()
	for _, s := range view.Steps {
		if s.ID == "look_bad" && (s.Evaluator != "model" || !s.Appraised || s.Verdict != "fail") {
			t.Fatalf("step view does not mark the model evaluation: %+v", s)
		}
		if s.ID == "play" && (s.Evaluator != "" || s.Appraised) {
			t.Fatalf("dynamic step view: %+v", s)
		}
	}
	if view.PendingReview != 4 {
		t.Fatalf("pending review = %d", view.PendingReview)
	}

	detail, err := hub.Detail(run.ID, "look_bad")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Appraisal == nil || detail.Appraisal.Verdict != "fail" || len(detail.Images) != 1 || !strings.HasPrefix(detail.Images[0].DataURI, "data:image/jpeg;base64,") || detail.Prompt != "Is the shop shown?" {
		t.Fatalf("step detail: %+v", detail)
	}

	review, err := hub.Start(context.Background(), StartArgs{Workflow: wf, Cwd: cwd, ReviewDeferred: true})
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]string{"look_ok": "pass", "look_bad": "pass", "play": "pass", "broken": "pass"}
	seen := map[string]*GateView{}
	deadline := time.Now().Add(5 * time.Second)
	for len(seen) < len(answers) && time.Now().Before(deadline) {
		g := review.view().Gate
		if g == nil || seen[g.StepID] != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		seen[g.StepID] = g
		if err := hub.Answer(review.ID, g.StepID, Answer{ChoiceID: answers[g.StepID]}); err != nil {
			t.Fatal(err)
		}
	}
	reviewed := review.Wait()
	if reviewed.Status != StatusPassed {
		t.Fatalf("review: %s %s", reviewed.Status, reviewed.FailedReason)
	}
	if g := seen["look_bad"]; g == nil || g.Appraisal == nil || g.Appraisal.Verdict != "fail" || !strings.Contains(g.Appraisal.Report, "settings menu") {
		t.Fatalf("owner gate did not show the model verdict: %+v", g)
	}
	if g := seen["play"]; g == nil || g.Appraisal != nil {
		t.Fatalf("dynamic gate carried an appraisal: %+v", g)
	}
	got := map[string]StepRecord{}
	for _, rec := range reviewed.Steps {
		got[rec.StepID] = rec
	}
	for id, want := range map[string]string{"look_ok": ModelConfirmed, "look_bad": ModelOverridden, "broken": ModelOverridden, "play": ""} {
		rec := got[id]
		if rec.AnsweredBy != AnsweredByOwner || rec.ChoiceID != "pass" || rec.ModelReview != want {
			t.Fatalf("%s owner review record: %+v", id, rec)
		}
	}
	img := got["look_bad"].Appraisal.Images
	if len(img) != 1 || !strings.HasPrefix(img[0], reviewed.ReportDir) {
		t.Fatalf("prepared images were not copied into the review bundle: %v", img)
	}
	if d, err := hub.Detail(review.ID, "look_bad"); err != nil || len(d.Images) != 1 || d.Images[0].DataURI == "" {
		t.Fatalf("review detail: %+v %v", d, err)
	}
	if views := hub.Snapshot().Runs; len(views) != 2 {
		t.Fatalf("prepared and reviewed runs must both stay for the owner: %d", len(views))
	}
}

func TestUnattendedAppraisalLeavesOwnerLockFree(t *testing.T) {
	release := make(chan struct{})
	hub := NewHub(HubArgs{Model: func(ctx context.Context, req StepRequest) ExecResult {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ExecResult{Code: 0, Output: "pass"}
	}})
	unattended, err := hub.Start(context.Background(), StartArgs{Workflow: loadWF(t, `
name: slow-appraisal
steps:
  - id: look
    type: human_gate
    judgment: static
    device: phone
    prompt: Look
    choices: [{id: pass, label: Yes}]
`), Cwd: t.TempDir(), DeferHumanGates: true})
	if err != nil {
		t.Fatal(err)
	}
	attended, err := hub.Start(context.Background(), StartArgs{Workflow: loadWF(t, `
name: attended
steps:
  - id: ask
    type: human_gate
    prompt: Ask
    choices: [{id: pass, label: Yes}]
`), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for attended.view().Gate == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if attended.view().Gate == nil {
		t.Fatal("an unattended appraisal held the daemon-wide owner gate lock")
	}
	close(release)
	if res := unattended.Wait(); res.Status != StatusPrepared {
		t.Fatalf("unattended: %+v", res)
	}
	_ = hub.Answer(attended.ID, "ask", Answer{ChoiceID: "pass"})
	attended.Wait()
}

func TestModelStepEvidenceIsRecorded(t *testing.T) {
	hub := NewHub(HubArgs{Model: func(_ context.Context, req StepRequest) ExecResult {
		return ExecResult{Code: 1, Output: "model response did not match accept: FAIL wrong screen", Model: &ModelEvidence{Provider: "claude", Model: "claude-test", Result: "FAIL wrong screen", Images: []string{"/etc/hosts"}}}
	}})
	res := runWF(t, hub, `
name: model-evidence
steps:
  - id: review
    type: model
    device: phone
    capture_screen: true
    prompt: Is it right?
    model: {purpose: analysis, quality: standard}
    accept: PASS
`, "", nil)
	if res.Status != StatusInvestigate || len(res.Steps) != 1 {
		t.Fatalf("result: %+v", res)
	}
	a := res.Steps[0].Appraisal
	if a == nil || a.Verdict != "rejected" || a.Report != "FAIL wrong screen" || a.Model != "claude-test" {
		t.Fatalf("model step evidence not on its record: %+v", a)
	}
	d, err := hub.Detail(res.RunID, "review")
	if err != nil || len(d.Images) != 1 || d.Images[0].DataURI != "" || d.Images[0].Error != "outside the run bundle" {
		t.Fatalf("detail must refuse images outside the bundle: %+v %v", d, err)
	}
}
