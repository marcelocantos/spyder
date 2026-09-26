// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerify_RESTAnswerPathAndResume(t *testing.T) {
	h := NewHandler()
	cwd := t.TempDir()
	wf := `
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
`
	res := dispatchJSONMap(t, h, "verify", map[string]any{
		"workflow": wf,
		"cwd":      cwd,
		"wait":     true,
		"answers":  map[string]any{"ask": map[string]any{"choice_id": "pass"}},
	})
	if res["status"] != "passed" {
		t.Fatalf("status=%v body=%v", res["status"], res)
	}
	if res["exit_code"].(float64) != 0 {
		t.Fatalf("exit_code=%v", res["exit_code"])
	}
	block, _ := res["status_block"].(string)
	for _, want := range []string{
		"======== STATUS passed ========",
		"workflow: sample",
		"device: handset",
		"choice=pass",
		"======== END STATUS ========",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("STATUS missing %q\n%s", want, block)
		}
	}

	marker := filepath.Join(cwd, "prep.log")
	wf2 := `
name: skip-sample
steps:
  - id: prep
    type: shell
    argv: ["/bin/sh", "-c", "echo prep >> ` + marker + `"]
`
	if r := dispatchJSONMap(t, h, "verify", map[string]any{"workflow": wf2, "cwd": cwd, "wait": true}); r["status"] != "passed" {
		t.Fatalf("first skip-sample: %v", r)
	}
	if r := dispatchJSONMap(t, h, "verify", map[string]any{"workflow": wf2, "cwd": cwd, "wait": true}); r["status"] != "passed" {
		t.Fatalf("second skip-sample: %v", r)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "prep") != 1 {
		t.Fatalf("prep ran %d times; pass record should skip", strings.Count(string(b), "prep"))
	}
}

func TestVerify_TwoGraphsHumanGateLockViaREST(t *testing.T) {
	h := NewHandler()
	cwd := t.TempDir()
	wfA := `
name: graph-a
steps:
  - id: gate-a
    type: human_gate
    prompt: A
    choices:
      - id: pass
        label: Yes
`
	wfB := `
name: graph-b
steps:
  - id: work
    type: shell
    argv: ["/bin/sleep", "0.3"]
  - id: gate-b
    type: human_gate
    requires: ["work"]
    prompt: B
    choices:
      - id: pass
        label: Yes
`
	a := dispatchJSONMap(t, h, "verify", map[string]any{"workflow": wfA, "cwd": cwd + "/a", "wait": false})
	b := dispatchJSONMap(t, h, "verify", map[string]any{"workflow": wfB, "cwd": cwd + "/b", "wait": false})
	runA, _ := a["run_id"].(string)
	runB, _ := b["run_id"].(string)
	if runA == "" || runB == "" {
		t.Fatalf("missing run ids A=%v B=%v", a, b)
	}

	deadline := time.Now().Add(3 * time.Second)
	var sawBWork bool
	for time.Now().Before(deadline) {
		snap := dispatchJSONMap(t, h, "verify_status", map[string]any{})
		gate, _ := snap["gate"].(map[string]any)
		if gate != nil && gate["step_id"] == "gate-a" {
			runs, _ := snap["runs"].([]any)
			for _, item := range runs {
				rm, _ := item.(map[string]any)
				if rm["run_id"] != runB {
					continue
				}
				steps, _ := rm["steps"].([]any)
				for _, s := range steps {
					sm := s.(map[string]any)
					if sm["id"] == "work" {
						st := sm["status"]
						if st == "running" || st == "ok" {
							sawBWork = true
						}
					}
					if sm["id"] == "gate-b" && sm["status"] == "waiting_human" {
						t.Fatal("B gate in flight while A holds the lock")
					}
				}
			}
			if sawBWork {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawBWork {
		t.Fatal("B command step did not run while A held the human_gate lock")
	}

	ans := dispatchJSONMap(t, h, "verify_answer", map[string]any{
		"run_id": runA, "gate_id": "gate-a", "choice_id": "pass",
	})
	if ans["ok"] != true {
		t.Fatalf("answer A: %v", ans)
	}

	deadline = time.Now().Add(3 * time.Second)
	answeredB := false
	for time.Now().Before(deadline) {
		snap := dispatchJSONMap(t, h, "verify_status", map[string]any{})
		gate, _ := snap["gate"].(map[string]any)
		if gate != nil && gate["step_id"] == "gate-b" && gate["run_id"] == runB {
			dispatchJSONMap(t, h, "verify_answer", map[string]any{
				"run_id": runB, "gate_id": "gate-b", "choice_id": "pass",
			})
			answeredB = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !answeredB {
		t.Fatal("B gate never became the in-flight gate")
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snap := dispatchJSONMap(t, h, "verify_status", map[string]any{})
		done := 0
		runs, _ := snap["runs"].([]any)
		for _, item := range runs {
			rm := item.(map[string]any)
			if rm["status"] == "passed" {
				done++
			}
		}
		if done == 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("runs did not both pass")
}

func TestVerify_SpyderScriptViaAppExec(t *testing.T) {
	h := NewHandler()
	cwd := t.TempDir()
	script := filepath.Join(cwd, "ok.star")
	if err := os.WriteFile(script, []byte("emit(\"script-ok\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wf := `
name: scripted
steps:
  - id: stage
    type: spyder_script
    script: ok.star
`
	res := dispatchJSONMap(t, h, "verify", map[string]any{
		"workflow": wf, "cwd": cwd, "wait": true,
	})
	if res["status"] != "passed" {
		t.Fatalf("status=%v reason=%v", res["status"], res["failed_reason"])
	}
}

func TestVerify_UnknownTypeRejected(t *testing.T) {
	h := NewHandler()
	r := dispatchJSON(t, h, "verify", map[string]any{
		"workflow": "name: x\nsteps:\n  - id: a\n    type: make\n    command: true\n",
		"wait":     true,
	})
	if !r.IsError {
		t.Fatal("want validation error")
	}
	text := resultText(t, &r)
	if !strings.Contains(strings.ToLower(text), "unknown type") {
		t.Fatalf("want unknown type, got %s", text)
	}
}

func TestVerify_ValidateOnly(t *testing.T) {
	h := NewHandler()
	res := dispatchJSONMap(t, h, "verify", map[string]any{
		"workflow":      "name: ok\nsteps:\n  - id: a\n    type: shell\n    argv: [\"/usr/bin/true\"]\n",
		"validate_only": true,
	})
	if res["ok"] != true || res["name"] != "ok" {
		t.Fatalf("validate_only: %v", res)
	}
}
