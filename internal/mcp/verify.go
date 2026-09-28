// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/verify"
)

// VerifyHub returns the daemon-wide verification scheduler, creating it
// on first use. spyder_script steps call app_exec in-process.
func (h *Handler) VerifyHub() *verify.Hub {
	h.verifyMu.Lock()
	defer h.verifyMu.Unlock()
	if h.verifyHub == nil {
		h.verifyHub = verify.NewHub(verify.HubArgs{
			Script:   h.execVerifyScript,
			Model:    h.execVerifyModel,
			StateDir: h.verifyStateDir,
		})
	}
	return h.verifyHub
}

// SetVerifyStateDir makes finished Verify runs survive a daemon restart by
// persisting the retained-run index under dir. Call before VerifyHub.
func (h *Handler) SetVerifyStateDir(dir string) {
	h.verifyMu.Lock()
	defer h.verifyMu.Unlock()
	h.verifyStateDir = dir
}

// verifyOwner is the caller's identity for run ownership: the explicit
// owner, else the basename of the caller's cwd, as with reservations.
func verifyOwner(args map[string]any) string {
	if owner := strings.TrimSpace(optString(args, "owner")); owner != "" {
		return owner
	}
	if cwd := optString(args, "cwd"); cwd != "" {
		return filepath.Base(cwd)
	}
	return ""
}

func (h *Handler) execVerifyScript(ctx context.Context, req verify.StepRequest) verify.ExecResult {
	script := req.Step.Script
	if script != "" && !filepath.IsAbs(script) {
		script = filepath.Join(req.Cwd, script)
	}
	args := map[string]any{"script_path": script}
	if len(req.Step.Params) > 0 {
		pm := map[string]any{}
		for k, v := range req.Step.Params {
			pm[k] = v
		}
		args["params"] = pm
	}
	if req.Timeout > 0 {
		args["max_duration_ms"] = float64(req.Timeout / time.Millisecond)
	}
	res, err := h.handleAppExecContext(ctx, args)
	if err != nil {
		return verify.ExecResult{Code: 1, Output: err.Error()}
	}
	var b strings.Builder
	if res != nil {
		for _, c := range res.Content {
			if tc, ok := c.(mcpgo.TextContent); ok {
				b.WriteString(tc.Text)
				b.WriteByte('\n')
				if req.Emit != nil {
					req.Emit(tc.Text)
				}
			}
		}
		if res.IsError {
			return verify.ExecResult{Code: 1, Output: b.String()}
		}
	}
	return verify.ExecResult{Code: 0, Output: b.String()}
}

func (h *Handler) handleVerify(args map[string]any) (*mcpgo.CallToolResult, error) {
	var raw []byte
	if p := optString(args, "workflow_path"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return toolErr("workflow_path: %v", err)
		}
		raw = b
	} else {
		text, err := requireString(args, "workflow")
		if err != nil {
			return toolErr("workflow or workflow_path is required")
		}
		raw = []byte(text)
	}
	wf, err := verify.Load(raw)
	if err != nil {
		return toolErr("%v", err)
	}
	if _, ok := args["validate_only"].(bool); ok && args["validate_only"].(bool) {
		return toolJSON(map[string]any{"ok": true, "name": wf.Name})
	}

	cwd := optString(args, "cwd")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	params := optStringMap(args, "params")
	answers := map[string]verify.Answer{}
	if rawAns, ok := args["answers"].(map[string]any); ok {
		for k, v := range rawAns {
			switch t := v.(type) {
			case string:
				answers[k] = verify.Answer{ChoiceID: t}
			case map[string]any:
				answers[k] = verify.Answer{
					ChoiceID: fmt.Sprint(t["choice_id"]),
					Comment:  fmt.Sprint(nilToEmpty(t["comment"])),
				}
			}
		}
	}
	allowWaive, _ := args["allow_waive"].(bool)
	deferHumanGates, _ := args["defer_human_gates"].(bool)
	reviewDeferred, _ := args["review_deferred"].(bool)
	wait := true
	if v, ok := args["wait"].(bool); ok {
		wait = v
	}

	run, err := h.VerifyHub().Start(context.Background(), verify.StartArgs{
		Workflow:        wf,
		WorkflowPath:    optString(args, "workflow_path"),
		Cwd:             cwd,
		Params:          params,
		Answers:         answers,
		AllowWaive:      allowWaive,
		DeferHumanGates: deferHumanGates,
		ReviewDeferred:  reviewDeferred,
		Owner:           verifyOwner(map[string]any{"owner": optString(args, "owner"), "cwd": cwd}),
	})
	if err != nil {
		return toolErr("%v", err)
	}
	if !wait {
		return toolJSON(map[string]any{"run_id": run.ID, "status": "running", "workflow": wf.Name})
	}
	res := run.Wait()
	return toolJSON(res)
}

func (h *Handler) handleVerifyStatus(args map[string]any) (*mcpgo.CallToolResult, error) {
	return toolJSON(h.VerifyHub().Snapshot())
}

func (h *Handler) handleVerifyAnswer(args map[string]any) (*mcpgo.CallToolResult, error) {
	runID, err := requireString(args, "run_id")
	if err != nil {
		return toolErr("%v", err)
	}
	gateID, err := requireString(args, "gate_id")
	if err != nil {
		return toolErr("%v", err)
	}
	choice, err := requireString(args, "choice_id")
	if err != nil {
		return toolErr("%v", err)
	}
	comment := optString(args, "comment")
	if err := h.VerifyHub().Answer(runID, gateID, verify.Answer{ChoiceID: choice, Comment: comment}); err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(map[string]any{"ok": true, "run_id": runID, "gate_id": gateID, "choice_id": choice})
}

func (h *Handler) handleVerifyAbort(args map[string]any) (*mcpgo.CallToolResult, error) {
	runID, err := requireString(args, "run_id")
	if err != nil {
		return toolErr("%v", err)
	}
	reason := optString(args, "reason")
	if reason == "" {
		reason = "aborted"
	}
	if err := h.VerifyHub().Abort(runID, reason); err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(map[string]any{"ok": true, "run_id": runID})
}

func (h *Handler) handleVerifyDismiss(args map[string]any) (*mcpgo.CallToolResult, error) {
	runID, err := requireString(args, "run_id")
	if err != nil {
		return toolErr("%v", err)
	}
	owner := verifyOwner(args)
	if owner == "" {
		return toolErr("verify_dismiss: owner or cwd is required to identify the run's creator")
	}
	if err := h.VerifyHub().Dismiss(runID, owner); err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(map[string]any{"ok": true, "run_id": runID})
}

func (h *Handler) handleVerifyDetail(args map[string]any) (*mcpgo.CallToolResult, error) {
	runID, err := requireString(args, "run_id")
	if err != nil {
		return toolErr("%v", err)
	}
	detail, err := h.VerifyHub().Detail(runID, optString(args, "step_id"))
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(detail)
}

func (h *Handler) handleVerifyReview(args map[string]any) (*mcpgo.CallToolResult, error) {
	runID, err := requireString(args, "run_id")
	if err != nil {
		return toolErr("%v", err)
	}
	stepID, err := requireString(args, "step_id")
	if err != nil {
		return toolErr("%v", err)
	}
	rev, err := h.VerifyHub().Review(runID, stepID, optString(args, "finding"), optString(args, "notes"))
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(map[string]any{"ok": true, "run_id": runID, "step_id": stepID, "review": rev})
}

func (h *Handler) handleVerifyReport(args map[string]any) (*mcpgo.CallToolResult, error) {
	runID, err := requireString(args, "run_id")
	if err != nil {
		return toolErr("%v", err)
	}
	report, err := h.VerifyHub().Report(runID)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(report)
}

func (h *Handler) handleVerifyNow(args map[string]any) (*mcpgo.CallToolResult, error) {
	runID, err := requireString(args, "run_id")
	if err != nil {
		return toolErr("%v", err)
	}
	stepID, err := requireString(args, "step_id")
	if err != nil {
		return toolErr("%v", err)
	}
	run, err := h.VerifyHub().VerifyNow(runID, stepID)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(map[string]any{"ok": true, "run_id": run.ID, "source_run_id": runID, "step_id": stepID, "status": "running"})
}

func nilToEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func verifyDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("verify",
			mcpgo.WithDescription("Run a product-neutral verification workflow YAML on the daemon-wide DAG scheduler (🎯T138). Step kinds: shell, spyder_script (in-process app_exec), human_gate. Pass wait=false to return a run_id immediately."),
			mcpgo.WithString("workflow",
				mcpgo.Description("Workflow YAML text (mutually exclusive with workflow_path)"),
			),
			mcpgo.WithString("workflow_path",
				mcpgo.Description("Path to a workflow YAML file"),
			),
			mcpgo.WithString("cwd",
				mcpgo.Description("Working directory for shell steps and the pass record (default: daemon cwd)"),
			),
			mcpgo.WithBoolean("wait",
				mcpgo.Description("Block until the run ends (default true)"),
			),
			mcpgo.WithBoolean("validate_only",
				mcpgo.Description("Check the workflow and return without running"),
			),
			mcpgo.WithBoolean("allow_waive",
				mcpgo.Description("Permit human_gate choices with outcome waive"),
			),
			mcpgo.WithBoolean("defer_human_gates",
				mcpgo.Description("Run unattended: continue through human gates without asking; record each as deferred, never passed. Final status is prepared (exit 4)."),
			),
			mcpgo.WithBoolean("review_deferred",
				mcpgo.Description("Replay owner checks from a prepared run. Reuse completed shell steps when the workflow and parameters match; rerun staging scripts, model checks, and review_replay shell steps. Model verdicts on static gates are shown to the owner to confirm or override."),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Creating agent's identity; only it may dismiss the finished run (default: basename of cwd)"),
			),
		),
		mcpgo.NewTool("verify_status",
			mcpgo.WithDescription("Snapshot of active and retained verification runs, plus the single in-flight human_gate if any. Finished runs stay until their creator calls verify_dismiss; their screenshots load through verify_detail."),
		),
		mcpgo.NewTool("verify_dismiss",
			mcpgo.WithDescription("Remove a finished verification run from the live view. The run bundle stays on disk. Refused while the run is active or when the caller is not the run's creator."),
			mcpgo.WithString("run_id", mcpgo.Required(), mcpgo.Description("Run id from verify")),
			mcpgo.WithString("owner", mcpgo.Description("Caller identity; must match the run's owner")),
			mcpgo.WithString("cwd", mcpgo.Description("Caller cwd; its basename is the owner when owner is omitted")),
		),
		mcpgo.NewTool("verify_review",
			mcpgo.WithDescription("Save the owner's review of a step awaiting it (a model result or a deferred owner gate): a finding and free-form notes. The dashboard saves as the owner types. Findings are the gate's choices, or pass/fail/unclear for a model step, plus in_game (Check in game) on every entry: the owner cannot judge it without seeing it in the running product. Empty finding and notes clear the review. It is evidence beside the run; the run status does not change."),
			mcpgo.WithString("run_id", mcpgo.Required(), mcpgo.Description("Run id")),
			mcpgo.WithString("step_id", mcpgo.Required(), mcpgo.Description("Step id or outline number (e.g. 1.2.3)")),
			mcpgo.WithString("finding", mcpgo.Description("Choice id; empty keeps notes as a draft")),
			mcpgo.WithString("notes", mcpgo.Description("Free-form notes, multiline")),
		),
		mcpgo.NewTool("verify_now",
			mcpgo.WithDescription("Put the app back into the state an entry awaiting review was judged in, and leave it there for the owner. Replays only the entry's staging scripts from the run's saved workflow and parameters: no builds, checks, gates or cleanup, and nothing is reassessed. Returns the new run's id; it ends 'staged' (or failed, in which case the owner checks by hand). A new request for the same entry replaces the previous one."),
			mcpgo.WithString("run_id", mcpgo.Required(), mcpgo.Description("Run id holding the entry")),
			mcpgo.WithString("step_id", mcpgo.Required(), mcpgo.Description("Step id or outline number")),
		),
		mcpgo.NewTool("verify_report",
			mcpgo.WithDescription("A run's full report as one JSON document: run facts and final status, the numbered outline, and every step with its definition, records, model appraisal (verdict, full report, model, image paths) and the owner's review (finding and notes), plus summary counts. Use verify_detail for one step's images inline."),
			mcpgo.WithString("run_id", mcpgo.Required(), mcpgo.Description("Run id")),
		),
		mcpgo.NewTool("verify_detail",
			mcpgo.WithDescription("One step's evidence on demand: its records, the model appraisal (verdict, full report, model identity), and every image the model reviewed as data URIs. Without step_id, the run's latest screenshot."),
			mcpgo.WithString("run_id", mcpgo.Required(), mcpgo.Description("Run id from verify")),
			mcpgo.WithString("step_id", mcpgo.Description("Step id; omit for the run's latest screenshot")),
		),
		mcpgo.NewTool("verify_answer",
			mcpgo.WithDescription("Answer the in-flight human_gate for a run."),
			mcpgo.WithString("run_id", mcpgo.Required(), mcpgo.Description("Run id from verify")),
			mcpgo.WithString("gate_id", mcpgo.Required(), mcpgo.Description("Step id of the gate")),
			mcpgo.WithString("choice_id", mcpgo.Required(), mcpgo.Description("Choice id from the gate")),
			mcpgo.WithString("comment", mcpgo.Description("Optional owner comment")),
		),
		mcpgo.NewTool("verify_abort",
			mcpgo.WithDescription("Abort a verification run."),
			mcpgo.WithString("run_id", mcpgo.Required(), mcpgo.Description("Run id from verify")),
			mcpgo.WithString("reason", mcpgo.Description("Abort reason")),
		),
	}
}
