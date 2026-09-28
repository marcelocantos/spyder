// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 🎯T149.1: a finished run stays in the hub, on disk and across a restart
// until the agent that created it dismisses it.
func TestFinishedRunStaysUntilItsCreatorDismissesIt(t *testing.T) {
	state := t.TempDir()
	cwd := t.TempDir()
	hub := NewHub(HubArgs{StateDir: state})
	wf := loadWF(t, `
name: persisted-run
params:
  device: iPad
steps:
  - id: check
    type: shell
    label: Check ${device}
    argv: [/usr/bin/true]
  - id: look
    type: human_gate
    requires: [check]
    prompt: Look at ${device}
    choices:
      - id: pass
        label: Pass
`)
	run, err := hub.Start(context.Background(), StartArgs{Workflow: wf, Cwd: cwd, Owner: "agent-a", Answers: map[string]Answer{"look": {ChoiceID: "pass"}}})
	if err != nil {
		t.Fatal(err)
	}
	result := run.Wait()
	if result.Status != StatusPassed || result.Owner != "agent-a" {
		t.Fatalf("run: status=%s owner=%q reason=%s", result.Status, result.Owner, result.FailedReason)
	}
	for filename, want := range map[string]string{
		"report.json":   `"status": "passed"`,
		"workflow.yaml": "name: persisted-run",
		"params.json":   `"device": "iPad"`,
		"events.log":    "passed persisted-run",
		runMetaFile:     `"owner": "agent-a"`,
	} {
		data, err := os.ReadFile(filepath.Join(result.ReportDir, filename))
		if err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("%s: err=%v content=%q", filename, err, data)
		}
	}

	views := hub.Snapshot().Runs
	if len(views) != 1 || views[0].Status != StatusPassed || views[0].Owner != "agent-a" || views[0].Steps[1].ChoiceID != "pass" || views[0].Steps[1].Evaluator != AnsweredByPreset {
		t.Fatalf("finished run not retained with its evidence: %+v", views)
	}

	restarted := NewHub(HubArgs{StateDir: state})
	views = restarted.Snapshot().Runs
	if len(views) != 1 {
		t.Fatalf("restart lost the retained run: %+v", views)
	}
	v := views[0]
	if v.RunID != run.ID || v.Status != StatusPassed || v.Steps[0].Label != "Check iPad" || v.Steps[0].Status != StepOK || v.Steps[1].ChoiceID != "pass" || len(v.Log) == 0 {
		t.Fatalf("rehydrated view: %+v", v)
	}
	if got := restarted.RunByID(run.ID).Wait(); got.Status != StatusPassed || len(got.Steps) != 2 {
		t.Fatalf("rehydrated result: %+v", got)
	}

	if err := restarted.Dismiss(run.ID, "agent-b"); err == nil || !strings.Contains(err.Error(), "agent-a") {
		t.Fatalf("another agent dismissed the run: %v", err)
	}
	if err := restarted.Dismiss(run.ID, "agent-a"); err != nil {
		t.Fatal(err)
	}
	if views := restarted.Snapshot().Runs; len(views) != 0 {
		t.Fatalf("dismissed run remains: %+v", views)
	}
	if _, err := os.Stat(filepath.Join(result.ReportDir, "report.json")); err != nil {
		t.Fatalf("dismissal removed the bundle: %v", err)
	}
	if views := NewHub(HubArgs{StateDir: state}).Snapshot().Runs; len(views) != 0 {
		t.Fatalf("dismissed run came back after restart: %+v", views)
	}
}

func TestDismissRefusesActiveRun(t *testing.T) {
	hub := NewHub(HubArgs{})
	cwd := filepath.Join(t.TempDir(), "project")
	run, err := hub.Start(context.Background(), StartArgs{Workflow: loadWF(t, `
name: waiting
steps:
  - id: look
    type: human_gate
    prompt: Look
    choices: [{id: pass, label: Pass}]
`), Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.Dismiss(run.ID, "project"); err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("active run dismissed: %v", err)
	}
	if err := hub.Abort(run.ID, "test done"); err != nil {
		t.Fatal(err)
	}
	if got := run.Wait(); got.Status != StatusAborted || got.Owner != "project" {
		t.Fatalf("aborted run: %+v", got)
	}
	if views := hub.Snapshot().Runs; len(views) != 1 || views[0].Status != StatusAborted {
		t.Fatalf("aborted run not retained: %+v", views)
	}
	if err := hub.Dismiss(run.ID, "project"); err != nil {
		t.Fatalf("owner could not dismiss (default owner is the cwd basename): %v", err)
	}
}

func TestRestartMarksInterruptedRunAborted(t *testing.T) {
	state := t.TempDir()
	hub := NewHub(HubArgs{StateDir: state})
	run, err := hub.Start(context.Background(), StartArgs{Workflow: loadWF(t, `
name: interrupted
steps:
  - id: look
    type: human_gate
    prompt: Look
    choices: [{id: pass, label: Pass}]
`), Cwd: t.TempDir(), Owner: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for run.view().Gate == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// A second hub on the same state models a daemon restart while the first
	// run still waits: its bundle has no report yet.
	views := NewHub(HubArgs{StateDir: state}).Snapshot().Runs
	if len(views) != 1 || views[0].Status != StatusAborted || views[0].Reason != interruptedReason {
		t.Fatalf("interrupted run: %+v", views)
	}
	if data, err := os.ReadFile(filepath.Join(views[0].ReportDir, "report.json")); err != nil || !strings.Contains(string(data), interruptedReason) {
		t.Fatalf("interrupted report: err=%v %s", err, data)
	}
	_ = hub.Abort(run.ID, "test done")
	run.Wait()
}
