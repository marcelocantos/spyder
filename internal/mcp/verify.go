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
			Script: h.execVerifyScript,
			Model:  h.execVerifyModel,
		})
	}
	return h.verifyHub
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
	wait := true
	if v, ok := args["wait"].(bool); ok {
		wait = v
	}

	run, err := h.VerifyHub().Start(context.Background(), verify.StartArgs{
		Workflow:     wf,
		WorkflowPath: optString(args, "workflow_path"),
		Cwd:          cwd,
		Params:       params,
		Answers:      answers,
		AllowWaive:   allowWaive,
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
		),
		mcpgo.NewTool("verify_status",
			mcpgo.WithDescription("Snapshot of in-flight and recent verification runs, plus the single in-flight human_gate if any."),
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
