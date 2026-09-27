// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/marcelocantos/claudia"
	"github.com/marcelocantos/spyder/internal/verify"
)

// execVerifyModel runs a bounded one-shot text task using Claudia's model
// predicates for selection. The model's final result is recorded in the
// Verify report and may be matched exactly before dependents can run.
func (h *Handler) execVerifyModel(ctx context.Context, req verify.StepRequest) verify.ExecResult {
	pred, err := claudia.DecodePredicatesWire(req.Step.ModelSpec)
	if err != nil {
		return verify.ExecResult{Code: 1, Output: err.Error()}
	}
	if pred.Mode != "" && pred.Mode != claudia.CapabilityTask {
		return verify.ExecResult{Code: 1, Output: "model: mode must be task"}
	}
	pred.Mode = claudia.CapabilityTask
	pick, err := claudia.Resolve(ctx, pred)
	if err != nil {
		return verify.ExecResult{Code: 1, Output: "claudia resolve: " + err.Error()}
	}
	if req.Emit != nil {
		req.Emit(fmt.Sprintf("Claudia selected %s %s (%s)", pick.Provider, pick.Model, pick.Reason))
	}
	prompt := req.Step.Prompt
	var screenshotPath string
	if req.Step.CaptureScreen {
		if pick.Provider != claudia.ProviderClaude {
			return verify.ExecResult{Code: 1, Output: fmt.Sprintf("model screen review needs Claude's image-reading tool; Claudia selected %s", pick.Provider)}
		}
		if err := ctx.Err(); err != nil {
			return verify.ExecResult{Code: 124, Output: err.Error(), TimedOut: true}
		}
		screenshotPath = filepath.Join(req.Env["SPYDER_RUN_DIR"], sanitizeFilename(req.Step.ID)+".png")
		h.mu.Lock()
		adapter, _, id, err := h.resolveAdapter(req.Step.Device)
		var png []byte
		if err == nil {
			png, err = adapter.Screenshot(id)
		}
		h.mu.Unlock()
		if err != nil {
			return verify.ExecResult{Code: 1, Output: "model screenshot: " + err.Error()}
		}
		if err := writeOutputFile(screenshotPath, png); err != nil {
			return verify.ExecResult{Code: 1, Output: "saving model screenshot: " + err.Error()}
		}
		if req.Emit != nil {
			req.Emit("captured current screen: " + screenshotPath)
		}
		prompt = fmt.Sprintf("Read the current physical screenshot at %s with the Read image tool before deciding. If the image cannot be opened, reject it.\n\n%s", screenshotPath, prompt)
	}
	cfg := claudia.TaskConfig{
		ID:       "spyder-verify-" + req.Step.ID,
		Name:     req.Step.Label,
		Provider: pick.Provider,
		Model:    pick.Model,
		WorkDir:  req.Cwd,
	}
	// Verification text tasks do not need to change project files or ask
	// for permissions. Unsupported providers are refused before spawning.
	switch pick.Provider {
	case claudia.ProviderClaude:
		cfg.DisallowTools = []string{"Bash", "Write", "Edit", "WebFetch", "WebSearch"}
		if screenshotPath == "" {
			cfg.DisallowTools = append(cfg.DisallowTools, "Read")
		}
	case claudia.ProviderCodex:
		cfg.SandboxMode = "read-only"
		cfg.ApprovalPolicy = "never"
	default:
		return verify.ExecResult{Code: 1, Output: fmt.Sprintf("model: selected provider %s has no Verify read-only policy", pick.Provider)}
	}
	task := claudia.NewTask(cfg)
	defer task.Stop()
	events, err := task.Run(ctx, prompt)
	if err != nil {
		return verify.ExecResult{Code: 1, Output: "claudia task: " + err.Error()}
	}
	var result string
	var taskErr string
	readScreenshot := false
	for ev := range events {
		switch ev.Type {
		case claudia.TaskEventToolUse:
			if ev.ToolName == "Read" && strings.Contains(ev.ToolInput, screenshotPath) && screenshotPath != "" {
				readScreenshot = true
			}
		case claudia.TaskEventResult:
			result = strings.TrimSpace(ev.Content)
		case claudia.TaskEventError:
			taskErr = ev.ErrorMsg
		}
	}
	if err := ctx.Err(); err != nil {
		return verify.ExecResult{Code: 124, Output: "model: " + err.Error(), TimedOut: true}
	}
	if taskErr != "" {
		return verify.ExecResult{Code: 1, Output: "claudia task: " + taskErr}
	}
	if result == "" {
		return verify.ExecResult{Code: 1, Output: "claudia task: no final result"}
	}
	if screenshotPath != "" && !readScreenshot {
		return verify.ExecResult{Code: 1, Output: "model did not read the current screenshot"}
	}
	if req.Emit != nil {
		req.Emit("model result: " + result)
	}
	if req.Step.Accept != "" && result != req.Step.Accept {
		return verify.ExecResult{Code: 1, Output: "model response did not match accept: " + result}
	}
	return verify.ExecResult{Code: 0, Output: result}
}
