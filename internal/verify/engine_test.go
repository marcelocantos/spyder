// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func loadWF(t *testing.T, yaml string) *Workflow {
	t.Helper()
	wf, err := Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return wf
}

func runWF(t *testing.T, hub *Hub, yaml string, cwd string, answers map[string]Answer) Result {
	t.Helper()
	if hub == nil {
		hub = NewHub(HubArgs{})
	}
	if cwd == "" {
		cwd = t.TempDir()
	}
	wf := loadWF(t, yaml)
	run, err := hub.Start(context.Background(), StartArgs{
		Workflow: wf,
		Cwd:      cwd,
		Answers:  answers,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return run.Wait()
}

func TestValidate_UnknownGroup(t *testing.T) {
	_, err := Load([]byte(`
name: sample
groups:
  - id: phone
    label: Phone
steps:
  - id: a
    type: shell
    group: missing
    argv: ["/usr/bin/true"]
`))
	if err == nil || !strings.Contains(err.Error(), "unknown group") {
		t.Fatalf("want unknown group, got %v", err)
	}
}

func TestValidate_RequiresCycle(t *testing.T) {
	_, err := Load([]byte(`
name: sample
steps:
  - id: a
    type: shell
    argv: ["/usr/bin/true"]
    requires: ["a"]
`))
	if err == nil || !strings.Contains(err.Error(), "requires cycle") {
		t.Fatalf("want requires cycle, got %v", err)
	}
}

func TestValidate_GroupParentCycle(t *testing.T) {
	_, err := Load([]byte(`
name: sample
groups:
  - id: a
    parent: b
  - id: b
    parent: a
steps:
  - id: s
    type: shell
    argv: ["/usr/bin/true"]
`))
	if err == nil || !strings.Contains(err.Error(), "group parent cycle") {
		t.Fatalf("want group parent cycle, got %v", err)
	}
}

func TestIndependentLeavesRunInParallel(t *testing.T) {
	started := time.Now()
	res := runWF(t, nil, `
name: sample
steps:
  - id: a
    type: shell
    argv: ["/bin/sleep", "0.4"]
  - id: b
    type: shell
    argv: ["/bin/sleep", "0.4"]
`, "", nil)
	if res.ExitCode != 0 || res.Status != StatusPassed {
		t.Fatalf("status=%s code=%d reason=%s", res.Status, res.ExitCode, res.FailedReason)
	}
	if elapsed := time.Since(started); elapsed >= 700*time.Millisecond {
		t.Fatalf("independent leaves took %s; want parallel (<700ms)", elapsed)
	}
}

func TestMutexSerializes(t *testing.T) {
	started := time.Now()
	res := runWF(t, nil, `
name: sample
steps:
  - id: a
    type: shell
    mutex: unity
    argv: ["/bin/sleep", "0.3"]
  - id: b
    type: shell
    mutex: unity
    argv: ["/bin/sleep", "0.3"]
`, "", nil)
	if res.ExitCode != 0 {
		t.Fatalf("code=%d reason=%s", res.ExitCode, res.FailedReason)
	}
	if elapsed := time.Since(started); elapsed < 550*time.Millisecond {
		t.Fatalf("mutex steps took %s; want serialized (>=550ms)", elapsed)
	}
}

func TestStatusBlockOnPass(t *testing.T) {
	res := runWF(t, nil, `
name: sample
params:
  device: handset
steps:
  - id: hello
    type: shell
    argv: ["/bin/echo", "hello"]
  - id: ask
    type: human_gate
    prompt: Look
    choices:
      - id: pass
        label: Yes
`, "", map[string]Answer{"ask": {ChoiceID: "pass"}})
	if res.ExitCode != 0 {
		t.Fatalf("code=%d %s", res.ExitCode, res.FailedReason)
	}
	block := res.StatusBlock
	for _, want := range []string{
		"======== STATUS passed ========",
		"workflow: sample",
		"device: handset",
		"hello  ok",
		"ask  ok",
		"choice=pass",
		"======== END STATUS ========",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("STATUS missing %q\n%s", want, block)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(block), "======== END STATUS ========") {
		t.Errorf("STATUS does not end with END STATUS:\n%s", block)
	}
}

func TestPassKeepsRecordSoNextRunSkips(t *testing.T) {
	cwd := t.TempDir()
	marker := filepath.Join(cwd, "prep.log")
	yaml := `
name: resume-sample
steps:
  - id: prep
    type: shell
    argv: ["/bin/sh", "-c", "echo prep >> ` + marker + `"]
`
	hub := NewHub(HubArgs{})
	res1 := runWF(t, hub, yaml, cwd, nil)
	if res1.ExitCode != 0 {
		t.Fatalf("first: %s", res1.FailedReason)
	}
	res2 := runWF(t, hub, yaml, cwd, nil)
	if res2.ExitCode != 0 {
		t.Fatalf("second: %s", res2.FailedReason)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "prep") != 1 {
		t.Fatalf("prep ran %d times; pass should keep the record", strings.Count(string(b), "prep"))
	}
}

func TestDeleteRecordReruns(t *testing.T) {
	cwd := t.TempDir()
	marker := filepath.Join(cwd, "prep.log")
	yaml := `
name: resume-sample
steps:
  - id: prep
    type: shell
    argv: ["/bin/sh", "-c", "echo prep >> ` + marker + `"]
  - id: boom
    type: shell
    requires: ["prep"]
    argv: ["/usr/bin/false"]
`
	hub := NewHub(HubArgs{})
	if res := runWF(t, hub, yaml, cwd, nil); res.ExitCode != ExitInvestigate {
		t.Fatalf("first code=%d", res.ExitCode)
	}
	path := ResumePath(cwd, "resume-sample")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pass record missing: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if res := runWF(t, hub, yaml, cwd, nil); res.ExitCode != ExitInvestigate {
		t.Fatalf("second code=%d", res.ExitCode)
	}
	b, _ := os.ReadFile(marker)
	if strings.Count(string(b), "prep") != 2 {
		t.Fatalf("after delete, prep ran %d times; want 2", strings.Count(string(b), "prep"))
	}
}

func TestResumeSkipsPassedAndRetriesFailure(t *testing.T) {
	cwd := t.TempDir()
	marker := filepath.Join(cwd, "prep.log")
	yaml := `
name: resume-sample
steps:
  - id: prep
    type: shell
    argv: ["/bin/sh", "-c", "echo prep >> ` + marker + `"]
  - id: boom
    type: shell
    requires: ["prep"]
    argv: ["/usr/bin/false"]
`
	hub := NewHub(HubArgs{})
	runWF(t, hub, yaml, cwd, nil)
	runWF(t, hub, yaml, cwd, nil)
	b, _ := os.ReadFile(marker)
	if strings.Count(string(b), "prep") != 1 {
		t.Fatalf("prep ran %d times; want skip on resume", strings.Count(string(b), "prep"))
	}
}

func TestRestageSpyderScriptRequiredByFailedGate(t *testing.T) {
	cwd := t.TempDir()
	var stages atomic.Int32
	hub := NewHub(HubArgs{
		Script: func(ctx context.Context, req StepRequest) ExecResult {
			stages.Add(1)
			return ExecResult{Code: 0}
		},
	})
	yaml := `
name: resume-sample
steps:
  - id: prep
    type: shell
    argv: ["/usr/bin/true"]
  - id: stage
    type: spyder_script
    script: recipe.star
    requires: ["prep"]
  - id: ask
    type: human_gate
    requires: ["stage"]
    prompt: Look
    choices:
      - id: pass
        label: Yes
      - id: fail
        label: No
        outcome: investigate
`
	ans := map[string]Answer{"ask": {ChoiceID: "fail"}}
	if res := runWF(t, hub, yaml, cwd, ans); res.ExitCode != ExitInvestigate {
		t.Fatalf("first: %d %s", res.ExitCode, res.FailedReason)
	}
	if res := runWF(t, hub, yaml, cwd, ans); res.ExitCode != ExitInvestigate {
		t.Fatalf("second: %d %s", res.ExitCode, res.FailedReason)
	}
	if stages.Load() != 2 {
		t.Fatalf("stage ran %d times; want restage on failed gate (2)", stages.Load())
	}
}

func TestHumanGateLockAcrossTwoGraphs(t *testing.T) {
	hub := NewHub(HubArgs{})
	cwd := t.TempDir()

	wfA := loadWF(t, `
name: graph-a
steps:
  - id: gate-a
    type: human_gate
    prompt: A
    choices:
      - id: pass
        label: Yes
`)
	wfB := loadWF(t, `
name: graph-b
steps:
  - id: work
    type: shell
    argv: ["/bin/sleep", "0.35"]
  - id: gate-b
    type: human_gate
    requires: ["work"]
    prompt: B
    choices:
      - id: pass
        label: Yes
`)

	runA, err := hub.Start(context.Background(), StartArgs{Workflow: wfA, Cwd: cwd + "/a"})
	if err != nil {
		t.Fatal(err)
	}
	runB, err := hub.Start(context.Background(), StartArgs{Workflow: wfB, Cwd: cwd + "/b"})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	sawAGate := false
	sawBWork := false
	for time.Now().Before(deadline) {
		snap := hub.Snapshot()
		var aGate, bGate bool
		var bWork string
		for _, v := range snap.Runs {
			if v.RunID == runA.ID {
				for _, s := range v.Steps {
					if s.ID == "gate-a" && (s.Status == StepWaiting || s.Status == StepRunning) {
						aGate = true
					}
				}
			}
			if v.RunID == runB.ID {
				for _, s := range v.Steps {
					if s.ID == "work" && (s.Status == StepRunning || s.Status == StepOK) {
						bWork = s.Status
					}
					if s.ID == "gate-b" && s.Status == StepWaiting {
						bGate = true
					}
				}
			}
		}
		if aGate && bWork != "" {
			sawAGate = true
			sawBWork = true
			if bGate {
				t.Fatal("graph-b human_gate started while graph-a still holds the lock")
			}
			if holder, ok := hub.pool.holding(resourceKeyHuman); !ok || holder != runA.ID {
				t.Fatalf("human_gate holder = %q ok=%v; want run A %s", holder, ok, runA.ID)
			}
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawAGate || !sawBWork {
		t.Fatal("timed out waiting for A gate + B command step")
	}

	if err := hub.Answer(runA.ID, "gate-a", Answer{ChoiceID: "pass"}); err != nil {
		t.Fatalf("answer A: %v", err)
	}
	resA := runA.Wait()
	if resA.ExitCode != 0 {
		t.Fatalf("A: %s %s", resA.Status, resA.FailedReason)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g := runB.view().Gate; g != nil && g.StepID == "gate-b" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if err := hub.Answer(runB.ID, "gate-b", Answer{ChoiceID: "pass"}); err != nil {
		t.Fatalf("answer B: %v", err)
	}
	resB := runB.Wait()
	if resB.ExitCode != 0 {
		t.Fatalf("B: %s %s", resB.Status, resB.FailedReason)
	}
}

func TestPackageHasNoProductNames(t *testing.T) {
	banned := []string{"minicades", "stockcar", "stock car", "stock-car", "nascar", "s24", "halloween", "jevons"}
	root := "."
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".md") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(b))
		for _, w := range banned {
			if strings.Contains(lower, w) {
				t.Errorf("%s names product %q", name, w)
			}
		}
	}
}
