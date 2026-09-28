// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
)

const verifyNowWorkflow = `
name: verify-now
steps:
  - {id: build, type: shell, argv: [/usr/bin/true]}
  - {id: stage1, type: spyder_script, requires: [build], script: s1.star}
  - {id: check1, type: model, requires: [stage1], prompt: "ok?", model: {purpose: analysis}}
  - {id: gate1, type: human_gate, requires: [check1], prompt: "One?", choices: [{id: pass, label: "Yes"}]}
  - {id: stage2, type: spyder_script, requires: [gate1], script: s2.star}
  - {id: settle2, type: spyder_script, requires: [stage2], script: s2b.star}
  - {id: confirm2, type: shell, requires: [settle2], review_replay: true, argv: [/usr/bin/true]}
  - {id: check2, type: model, requires: [confirm2], prompt: "ok?", model: {purpose: analysis}}
  - {id: gate2, type: human_gate, requires: [check2], prompt: "Two?", choices: [{id: pass, label: "Yes"}]}
  - {id: after, type: shell, requires: [gate2], argv: [/usr/bin/true]}
cleanup:
  - {id: stop, type: shell, argv: [/usr/bin/true]}
`

// Verify Now replays only an entry's staging scripts from a prepared run and
// leaves the app in that state: it reassesses nothing (no checks, gates or
// cleanup), writes no review, and leaves the workflow's resume record alone.
func TestVerifyNowOnlyStagesTheEntry(t *testing.T) {
	var mu sync.Mutex
	ran := map[string]int{}
	count := func(_ context.Context, req StepRequest) ExecResult {
		mu.Lock()
		ran[req.Step.ID]++
		mu.Unlock()
		return ExecResult{Output: "PASS", Model: &ModelEvidence{Result: "PASS"}}
	}
	hub := NewHub(HubArgs{Shell: count, Script: count, Model: count})
	cwd := t.TempDir()
	wf := loadWF(t, verifyNowWorkflow)
	if got := StagingChain(wf, "gate2"); len(got) != 2 || !got["stage2"] || !got["settle2"] {
		t.Fatalf("staging chain: %v", got)
	}
	if got := StagingChain(wf, "check1"); len(got) != 1 || !got["stage1"] {
		t.Fatalf("staging chain for a model step: %v", got)
	}
	prepared, err := hub.Start(context.Background(), StartArgs{Workflow: wf, Cwd: cwd, DeferHumanGates: true, Owner: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if res := prepared.Wait(); res.Status != StatusPrepared {
		t.Fatalf("prepared: %+v", res)
	}
	record, _ := os.ReadFile(ResumePath(cwd, wf.Name))
	mu.Lock()
	ran = map[string]int{}
	mu.Unlock()

	if _, err := hub.VerifyNow(prepared.ID, "build"); err == nil {
		t.Fatal("verify now accepted a step not awaiting review")
	}
	first, err := hub.VerifyNow(prepared.ID, "9") // outline number of gate2
	if err != nil {
		t.Fatal(err)
	}
	if res := first.Wait(); res.Status != StatusStaged || res.ExitCode != ExitPassed {
		t.Fatalf("verify now result: %+v", res)
	}
	mu.Lock()
	if ran["stage2"] != 1 || ran["settle2"] != 1 || len(ran) != 2 {
		t.Fatalf("verify now must run only staging scripts, not checks or cleanup: %v", ran)
	}
	mu.Unlock()
	if v := first.view(); v.Focus != "gate2" || v.FocusSource != prepared.ID || v.Owner != "agent" || v.Gate != nil {
		t.Fatalf("verify now view: %+v", v)
	}
	if d, _ := hub.Detail(prepared.ID, "gate2"); d.Review != nil {
		t.Fatalf("verify now recorded a review: %+v", d.Review)
	}
	if after, _ := os.ReadFile(ResumePath(cwd, wf.Name)); !bytes.Equal(record, after) {
		t.Fatalf("verify now changed the resume record:\n%s\n---\n%s", record, after)
	}

	second, err := hub.VerifyNow(prepared.ID, "gate2")
	if err != nil {
		t.Fatal(err)
	}
	second.Wait()
	if hub.RunByID(first.ID) != nil {
		t.Fatal("a repeat Verify Now kept the previous run for the same entry")
	}
	if err := hub.Dismiss(prepared.ID, "agent"); err != nil {
		t.Fatal(err)
	}
	if hub.RunByID(second.ID) != nil {
		t.Fatal("dismissing the run kept its Verify Now run")
	}
}
