// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/marcelocantos/claudia"
	"github.com/marcelocantos/spyder/internal/verify"
	"golang.org/x/image/draw"
)

// Claude reads images at up to 1568 px on the long edge, so a larger copy
// adds nothing the model can see. Claudia v0.44 also scans Claude's JSONL
// output with a 1 MiB line limit, and the Read tool's result line carries the
// base64 image more than once: a 485 KB iPad JPEG produced no final result
// (🎯T149) while 240 KB Android images passed. Keep the full PNG in the run
// bundle and give the model a resized copy well inside that budget.
const (
	maxModelImageEdge  = 1568
	maxModelImageBytes = 256 * 1024
)

func modelImage(pngData []byte) ([]byte, error) {
	source, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, fmt.Errorf("decode screenshot PNG: %w", err)
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if long := max(width, height); long > maxModelImageEdge {
		width, height = width*maxModelImageEdge/long, height*maxModelImageEdge/long
	}
	for {
		scaled := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.CatmullRom.Scale(scaled, scaled.Bounds(), source, bounds, draw.Src, nil)
		for _, quality := range []int{75, 60, 45, 30} {
			var out bytes.Buffer
			if err := jpeg.Encode(&out, scaled, &jpeg.Options{Quality: quality}); err != nil {
				return nil, fmt.Errorf("encode model JPEG: %w", err)
			}
			if out.Len() <= maxModelImageBytes {
				return out.Bytes(), nil
			}
		}
		if width/2 < 640 || height/2 < 360 {
			return nil, fmt.Errorf("model JPEG remains over %d bytes at %dx%d", maxModelImageBytes, width, height)
		}
		width, height = width*3/4, height*3/4
	}
}

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
	evidence := &verify.ModelEvidence{Provider: fmt.Sprint(pick.Provider), Model: pick.Model}
	prompt := req.Step.Prompt
	var modelImagePath string
	if req.Step.CaptureScreen {
		if pick.Provider != claudia.ProviderClaude {
			return verify.ExecResult{Code: 1, Output: fmt.Sprintf("model screen review needs Claude's image-reading tool; Claudia selected %s", pick.Provider), Model: evidence}
		}
		if err := ctx.Err(); err != nil {
			return verify.ExecResult{Code: 124, Output: err.Error(), TimedOut: true, Model: evidence}
		}
		shotDir := req.Env["SPYDER_RUN_DIR"]
		stem := sanitizeFilename(req.Step.ID)
		screenshotPath := filepath.Join(shotDir, stem+".png")
		modelImagePath = filepath.Join(filepath.Dir(shotDir), "model-inputs", stem+".jpg")
		var pngData []byte
		var err error
		if req.Step.ScreenPNG != "" {
			// Judge a frame another check already captured.
			pngData, err = os.ReadFile(req.Step.ScreenPNG)
		} else {
			h.mu.Lock()
			adapter, _, id, resolveErr := h.resolveAdapter(req.Step.Device)
			err = resolveErr
			if err == nil {
				pngData, err = adapter.Screenshot(id)
			}
			h.mu.Unlock()
		}
		if err != nil {
			return verify.ExecResult{Code: 1, Output: "model screenshot: " + err.Error(), Model: evidence}
		}
		if err := writeOutputFile(screenshotPath, pngData); err != nil {
			return verify.ExecResult{Code: 1, Output: "saving model screenshot: " + err.Error(), Model: evidence}
		}
		jpegData, err := modelImage(pngData)
		if err != nil {
			return verify.ExecResult{Code: 1, Output: "preparing model screenshot: " + err.Error(), Model: evidence}
		}
		if err := writeOutputFile(modelImagePath, jpegData); err != nil {
			return verify.ExecResult{Code: 1, Output: "saving model image: " + err.Error(), Model: evidence}
		}
		evidence.Images = []string{modelImagePath}
		if req.Emit != nil {
			req.Emit("captured current screen: " + screenshotPath)
			req.Emit(fmt.Sprintf("model image: %s (%d bytes)", modelImagePath, len(jpegData)))
		}
		prompt = fmt.Sprintf("Read the current physical screenshot at %s with the Read image tool before deciding. If the image cannot be opened, reject it.\n\n%s", modelImagePath, prompt)
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
		if modelImagePath == "" {
			cfg.DisallowTools = append(cfg.DisallowTools, "Read")
		}
	case claudia.ProviderCodex:
		cfg.SandboxMode = "read-only"
		cfg.ApprovalPolicy = "never"
	default:
		return verify.ExecResult{Code: 1, Output: fmt.Sprintf("model: selected provider %s has no Verify read-only policy", pick.Provider), Model: evidence}
	}
	task := claudia.NewTask(cfg)
	defer task.Stop()
	events, err := task.Run(ctx, prompt)
	if err != nil {
		return verify.ExecResult{Code: 1, Output: "claudia task: " + err.Error(), Model: evidence}
	}
	var result string
	var taskErr string
	readScreenshot := false
	for ev := range events {
		switch ev.Type {
		case claudia.TaskEventToolUse:
			if ev.ToolName == "Read" && strings.Contains(ev.ToolInput, modelImagePath) && modelImagePath != "" {
				readScreenshot = true
			}
		case claudia.TaskEventResult:
			result = strings.TrimSpace(ev.Content)
		case claudia.TaskEventError:
			taskErr = ev.ErrorMsg
		}
	}
	evidence.Result = result
	if err := ctx.Err(); err != nil {
		return verify.ExecResult{Code: 124, Output: "model: " + err.Error(), TimedOut: true, Model: evidence}
	}
	if taskErr != "" {
		return verify.ExecResult{Code: 1, Output: "claudia task: " + taskErr, Model: evidence}
	}
	if result == "" {
		return verify.ExecResult{Code: 1, Output: "claudia task: no final result", Model: evidence}
	}
	if modelImagePath != "" && !readScreenshot {
		return verify.ExecResult{Code: 1, Output: "model did not read the current screenshot", Model: evidence}
	}
	if req.Emit != nil {
		req.Emit("model result: " + result)
	}
	if req.Step.Accept != "" && result != req.Step.Accept {
		return verify.ExecResult{Code: 1, Output: "model response did not match accept: " + result, Model: evidence}
	}
	return verify.ExecResult{Code: 0, Output: result, Model: evidence}
}
