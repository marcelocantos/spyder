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

// screenModel mimics the daemon's model runner for screen checks: it saves
// the "captured" frame under the step's ID and answers from replies, which
// are consumed in order per step (the last one repeats).
type screenModel struct {
	mu      sync.Mutex
	replies map[string][]string
	calls   []StepRequest
}

func (m *screenModel) run(_ context.Context, req StepRequest) ExecResult {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	queue := m.replies[req.Step.ID]
	reply := "PASS"
	if len(queue) > 0 {
		reply = queue[0]
		if len(queue) > 1 {
			m.replies[req.Step.ID] = queue[1:]
		}
	}
	m.mu.Unlock()
	shots := req.Env["SPYDER_RUN_DIR"]
	_ = os.MkdirAll(shots, 0o755)
	_ = os.WriteFile(filepath.Join(shots, req.Step.ID+".png"), []byte("frame "+req.Step.ID), 0o644)
	if reply == "ERROR" {
		return ExecResult{Code: 1, Output: "claudia task: no final result", Model: &ModelEvidence{Model: "m"}}
	}
	return ExecResult{Output: reply, Model: &ModelEvidence{Model: "m", Result: reply}}
}

func (m *screenModel) callsFor(id string) []StepRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StepRequest
	for _, c := range m.calls {
		if c.Step.ID == id {
			out = append(out, c)
		}
	}
	return out
}

const preconditionWorkflow = `
name: precondition
defaults:
  screen_model: {purpose: analysis, quality: economy, prefer_provider: claude}
steps:
  - id: look
    type: human_gate
    device: phone
    judgment: static
    precondition:
      screen: A live race with cars on track
      mutex: unity
      retry: {count: 2, backoff_sec: 0}
    prompt: Does the lighting look right?
    choices:
      - {id: pass, label: "Yes"}
      - {id: fail, label: "No"}
  - id: after
    type: shell
    requires: [look]
    argv: [/usr/bin/true]
`

// A precondition never blocks: a persistent "not met" still opens the gate,
// carrying the model's reason as a warning.
func TestPreconditionNotMetWarnsButNeverBlocks(t *testing.T) {
	m := &screenModel{replies: map[string][]string{"look-precondition": {"FAIL — the results screen is showing"}}}
	hub := NewHub(HubArgs{Model: m.run})
	run, err := hub.Start(context.Background(), StartArgs{Workflow: loadWF(t, preconditionWorkflow), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for run.view().Gate == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	g := run.view().Gate
	if g == nil || g.Precondition == nil || g.Precondition.Status != PreconditionNotMet || g.Precondition.Attempts != 3 || !strings.Contains(g.Precondition.Reason, "results screen") {
		t.Fatalf("gate must open with a not-met warning after rechecks: %+v", g)
	}
	calls := m.callsFor("look-precondition")
	if len(calls) != 3 || !strings.Contains(string(calls[0].Step.ModelSpec), "economy") || !strings.Contains(calls[0].Step.Prompt, "A live race with cars on track") {
		t.Fatalf("precondition calls: %d %+v", len(calls), calls)
	}
	if err := hub.Answer(run.ID, "look", Answer{ChoiceID: "pass"}); err != nil {
		t.Fatal(err)
	}
	res := run.Wait()
	if res.Status != StatusPassed || res.Steps[0].Precondition == nil || res.Steps[0].Precondition.Status != PreconditionNotMet {
		t.Fatalf("result: %+v", res)
	}
	if !strings.Contains(res.StatusBlock, "precondition=not_met") {
		t.Fatalf("STATUS block hides the warning:\n%s", res.StatusBlock)
	}
	if v := run.view(); v.Steps[0].Precondition != PreconditionNotMet || v.Steps[0].Reviewable {
		t.Fatalf("view: %+v", v.Steps[0])
	}
}

func TestPreconditionRechecksTransientScreensAndToleratesModelErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replies  []string
		status   string
		attempts int
	}{
		{"loading screen clears", []string{"FAIL loading", "PASS"}, PreconditionMet, 2},
		{"model keeps failing", []string{"ERROR"}, PreconditionUnchecked, 3},
		{"unparseable reply", []string{"Looks like a race to me"}, PreconditionUnchecked, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &screenModel{replies: map[string][]string{"look-precondition": tc.replies}}
			hub := NewHub(HubArgs{Model: m.run})
			res := runWF(t, hub, preconditionWorkflow, "", map[string]Answer{"look": {ChoiceID: "pass"}})
			p := res.Steps[0].Precondition
			if res.Status != StatusPassed || p == nil || p.Status != tc.status || p.Attempts != tc.attempts {
				t.Fatalf("status=%s precondition=%+v", res.Status, p)
			}
		})
	}
}

// Unattended, a static gate's appraisal judges the frame the precondition
// captured, and a not-met precondition does not skip the appraisal.
func TestPreconditionFrameFeedsTheAppraisal(t *testing.T) {
	m := &screenModel{replies: map[string][]string{"look-precondition": {"FAIL unsure"}, "look": {"pass\nLighting reads well."}}}
	hub := NewHub(HubArgs{Model: m.run})
	run, err := hub.Start(context.Background(), StartArgs{Workflow: loadWF(t, preconditionWorkflow), Cwd: t.TempDir(), DeferHumanGates: true})
	if err != nil {
		t.Fatal(err)
	}
	res := run.Wait()
	rec := res.Steps[0]
	if res.Status != StatusPrepared || rec.Precondition == nil || rec.Precondition.Status != PreconditionNotMet || rec.Appraisal == nil || rec.Appraisal.Verdict != "pass" {
		t.Fatalf("unattended gate: %+v", rec)
	}
	appraisal := m.callsFor("look")
	if len(appraisal) != 1 || appraisal[0].Step.ScreenPNG != rec.Precondition.Screenshot || !strings.HasSuffix(rec.Precondition.Screenshot, "look-precondition.png") {
		t.Fatalf("appraisal did not reuse the precondition frame: %+v vs %q", appraisal, rec.Precondition.Screenshot)
	}
	d, err := hub.Detail(run.ID, "look")
	if err != nil || d.Precondition == nil || d.Precondition.Expected != "A live race with cars on track" {
		t.Fatalf("detail front matter: %+v %v", d, err)
	}
}

// The precondition holds its mutex only while checking, never while the
// owner looks.
func TestPreconditionReleasesItsMutexBeforeTheOwnerLooks(t *testing.T) {
	m := &screenModel{replies: map[string][]string{}}
	hub := NewHub(HubArgs{Model: m.run})
	run, err := hub.Start(context.Background(), StartArgs{Workflow: loadWF(t, preconditionWorkflow), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for run.view().Gate == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if holder, held := hub.pool.holding("mutex:unity"); held {
		t.Fatalf("mutex unity still held by %s while the owner looks", holder)
	}
	_ = hub.Answer(run.ID, "look", Answer{ChoiceID: "pass"})
	run.Wait()
}

func TestValidatePrecondition(t *testing.T) {
	for _, tc := range []struct{ yaml, want string }{
		{"name: x\nsteps:\n  - id: g\n    type: human_gate\n    precondition: {screen: Menu}\n    prompt: P\n    choices: [{id: pass, label: Yes}]\n", "precondition requires device"},
		{"name: x\nsteps:\n  - id: g\n    type: human_gate\n    device: d\n    precondition: {}\n    prompt: P\n    choices: [{id: pass, label: Yes}]\n", "precondition.screen"},
		{"name: x\nsteps:\n  - id: g\n    type: human_gate\n    device: d\n    precondition: {screen: Menu, colour: red}\n    prompt: P\n    choices: [{id: pass, label: Yes}]\n", "unknown precondition field colour"},
		{"name: x\ndefaults: {screen_model: {colour: red}}\nsteps:\n  - id: s\n    type: shell\n    argv: [/usr/bin/true]\n", "unknown Claudia model predicate colour"},
	} {
		if _, err := Load([]byte(tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err=%v want %q", err, tc.want)
		}
	}
}
