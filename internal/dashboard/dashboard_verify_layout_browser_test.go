// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package dashboard_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/marcelocantos/spyder/internal/dashboard"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/rest"
	"github.com/marcelocantos/spyder/internal/verify"
)

// TestDashboard_VerifyTabLayoutAndSelectionInRealBrowser is the committed
// real-browser oracle for 🎯T150: three seeded runs (running with a gate,
// prepared with model appraisals, passed), layout without horizontal overflow
// at desktop and phone widths, selection that survives live snapshots, and
// refusal to answer another run's gate from the wrong tab.
func TestDashboard_VerifyTabLayoutAndSelectionInRealBrowser(t *testing.T) {
	chromePath := findChrome(t)

	stubModel := func(_ context.Context, req verify.StepRequest) verify.ExecResult {
		reply := "PASS\nModel appraisal for " + req.Step.ID
		return verify.ExecResult{
			Code:   0,
			Output: reply,
			Model:  &verify.ModelEvidence{Provider: "test", Model: "browser-stub", Result: reply},
		}
	}
	h := spydermcp.NewHandler(spydermcp.WithVerifyModel(stubModel))
	mux := http.NewServeMux()
	mux.Handle(rest.Prefix, rest.NewHandler(h))
	dash := dashboard.NewHandler()
	mux.Handle(dashboard.Path, dash)
	mux.Handle(dashboard.Path+"/", dash)
	mux.HandleFunc(verify.WSPath, h.VerifyHub().HandleWS)
	mux.HandleFunc(verify.ReportPathPrefix, h.VerifyHub().HandleReport)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cwd := t.TempDir()
	passedWF := `
name: t150-passed
steps:
  - id: ok
    type: shell
    argv: ["/usr/bin/true"]
`
	preparedWF := `
name: t150-prepared
params:
  device: handset
steps:
  - id: look
    type: human_gate
    judgment: static
    device: ${device}
    prompt: Garage visible on ${device}?
    choices:
      - id: pass
        label: Yes
      - id: fail
        label: No
        outcome: investigate
`
	runningWF := `
name: t150-running
steps:
  - id: ask
    type: human_gate
    prompt: Waiting for owner?
    choices:
      - id: pass
        label: Yes
`

	passed := postTool(t, srv.URL, "verify", map[string]any{
		"workflow": passedWF, "cwd": cwd, "wait": true,
	})
	passedID, _ := passed["run_id"].(string)
	if passedID == "" {
		t.Fatalf("passed run: %v", passed)
	}

	prepared := postTool(t, srv.URL, "verify", map[string]any{
		"workflow": preparedWF, "cwd": cwd, "defer_human_gates": true, "wait": true,
	})
	preparedID, _ := prepared["run_id"].(string)
	if preparedID == "" {
		t.Fatalf("prepared start: %v", prepared)
	}
	waitRunStatus(t, srv.URL, preparedID, "prepared", 5*time.Second)

	running := postTool(t, srv.URL, "verify", map[string]any{
		"workflow": runningWF, "cwd": cwd, "wait": false,
	})
	runningID, _ := running["run_id"].(string)
	if runningID == "" {
		t.Fatalf("running start: %v", running)
	}
	waitRunGate(t, srv.URL, runningID, "ask", 3*time.Second)

	t.Cleanup(func() {
		for _, id := range []string{runningID, preparedID, passedID} {
			_ = h.VerifyHub().Abort(id, "test done")
			if r := h.VerifyHub().RunByID(id); r != nil {
				r.Wait()
			}
		}
	})

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromePath),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...)
	defer cancelAlloc()
	browser, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	openVerify := func(ctx context.Context, width, height int64) error {
		url := srv.URL + dashboard.Path + "#verify/" + passedID
		return chromedp.Do(ctx,
			chromedp.EmulateViewport(width, height),
			chromedp.Navigate(url),
			chromedp.WaitVisible(`#verify-tabs .vtab`),
			chromedp.Sleep(500*time.Millisecond),
		)
	}

	for _, vp := range []struct {
		name          string
		width, height int64
	}{
		{"desktop", 1440, 900},
		{"phone", 375, 812},
	} {
		ctx, cancel := context.WithTimeout(browser, 45*time.Second)
		defer cancel()
		if err := openVerify(ctx, vp.width, vp.height); err != nil {
			t.Fatalf("%s: open verify: %v", vp.name, err)
		}

		tabCount, err := chromedp.Run(ctx, chromedp.Evaluate[int](`document.querySelectorAll("#verify-tabs .vtab").length`))
		if err != nil {
			t.Fatalf("%s: tab count: %v", vp.name, err)
		}
		if tabCount < 3 {
			t.Fatalf("%s: expected at least 3 run tabs, got %d", vp.name, tabCount)
		}

		overflow, err := chromedp.Run(ctx, chromedp.Evaluate[int](`(() => {
  const root = document.documentElement;
  let worst = Math.max(0, root.scrollWidth - root.clientWidth);
  for (const el of document.querySelectorAll("#tab-verify, #verify-tabs, .verify-run, .verify-grid")) {
    const o = el.scrollWidth - el.clientWidth;
    if (o > worst) worst = o;
  }
  return worst;
})()`))
		if err != nil {
			t.Fatalf("%s: overflow measure: %v", vp.name, err)
		}
		if overflow > 0 {
			t.Errorf("%s: horizontal overflow %dpx in verify layout", vp.name, overflow)
		}
	}

	ctx, cancel := context.WithTimeout(browser, 60*time.Second)
	defer cancel()
	if err := openVerify(ctx, 1440, 900); err != nil {
		t.Fatalf("selection setup: %v", err)
	}

	selTab := fmt.Sprintf(`#verify-tabs .vtab[data-run-id="%s"]`, passedID)
	selStep := `#tab-verify .vstep[data-step-id="ok"]`
	if err := chromedp.Do(ctx,
		chromedp.Click(selTab),
		chromedp.Click(selStep),
		chromedp.WaitVisible(`#tab-verify .vstep.sel[data-step-id="ok"]`),
	); err != nil {
		t.Fatalf("select tab/step: %v", err)
	}

	extra := postTool(t, srv.URL, "verify", map[string]any{
		"workflow": passedWF, "cwd": cwd, "wait": true,
	})
	extraID, _ := extra["run_id"].(string)
	if extraID == "" {
		t.Fatalf("extra passed run: %v", extra)
	}
	t.Cleanup(func() {
		_ = h.VerifyHub().Abort(extraID, "test done")
		if r := h.VerifyHub().RunByID(extraID); r != nil {
			r.Wait()
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tabs, err := chromedp.Run(ctx, chromedp.Evaluate[int](`document.querySelectorAll("#verify-tabs .vtab").length`))
		if err != nil {
			t.Fatalf("tab count while waiting: %v", err)
		}
		if tabs >= 4 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	selJSON, err := chromedp.Run(ctx, chromedp.Evaluate[string](`(() => {
  const tab = document.querySelector("#verify-tabs .vtab.sel");
  const step = document.querySelector("#tab-verify .vstep.sel");
  return JSON.stringify({run: tab && tab.dataset.runId || "", step: step && step.dataset.stepId || ""});
})()`))
	if err != nil {
		t.Fatalf("read selection: %v", err)
	}
	var sel struct{ Run, Step string }
	if err := json.Unmarshal([]byte(selJSON), &sel); err != nil {
		t.Fatalf("selection json: %v", err)
	}
	selectedRun, selectedStep := sel.Run, sel.Step
	if selectedRun != passedID || selectedStep != "ok" {
		t.Fatalf("snapshot moved selection: tab=%q step=%q want tab=%q step=ok", selectedRun, selectedStep, passedID)
	}

	prepTab := fmt.Sprintf(`#verify-tabs .vtab[data-run-id="%s"]`, preparedID)
	if err := chromedp.Do(ctx,
		chromedp.Click(prepTab),
		chromedp.WaitVisible(`#tab-verify .vstep[data-step-id="look"] .vwho.model`),
	); err != nil {
		t.Fatalf("prepared model marker: %v", err)
	}

	gateErr, err := chromedp.Run(ctx, chromedp.Evaluate[string](`(async () => {
  let asking = null;
  for (const run of verifyRuns.values()) {
    if (run.gate) { asking = run; break; }
  }
  if (!asking || !asking.gate) return "no asking run";
  const choice = (asking.gate.choices || [])[0];
  if (!choice) return "no choices";
  return answerVerifyGate(asking.gate, choice, verifyCard).then(() => {
    const el = verifyCard.error;
    return el ? el.textContent : "";
  });
})()`, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("gate refusal evaluate: %v", err)
	}
	if !strings.Contains(gateErr, "another run") {
		t.Fatalf("gate refusal message = %q", gateErr)
	}

	gateVisible, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`!document.querySelector("#tab-verify .gate-sheet").classList.contains("hidden")`))
	if err != nil {
		t.Fatalf("gate visibility: %v", err)
	}
	if gateVisible {
		t.Error("non-selected run gate sheet was visible")
	}

	snap := postTool(t, srv.URL, "verify_status", nil)
	gate, _ := snap["gate"].(map[string]any)
	if gate == nil || gate["run_id"] != runningID {
		t.Fatalf("running gate should still be open: %v", snap["gate"])
	}
}

func waitRunStatus(t *testing.T, base, runID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap := postTool(t, base, "verify_status", nil)
		for _, item := range asSlice(snap["runs"]) {
			rm, _ := item.(map[string]any)
			if rm["run_id"] == runID && rm["status"] == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s never reached status %q", runID, want)
}

func waitRunGate(t *testing.T, base, runID, stepID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap := postTool(t, base, "verify_status", nil)
		gate, _ := snap["gate"].(map[string]any)
		if gate != nil && gate["run_id"] == runID && gate["step_id"] == stepID {
			return
		}
		for _, item := range asSlice(snap["runs"]) {
			rm, _ := item.(map[string]any)
			if rm["run_id"] == runID {
				g, _ := rm["gate"].(map[string]any)
				if g != nil && g["step_id"] == stepID {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s never showed gate %q", runID, stepID)
}
