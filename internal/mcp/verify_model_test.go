// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/verify"
)

func TestModelImageFitsClaudeTaskStream(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2200, 1200))
	rng := rand.New(rand.NewSource(1))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255})
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	compressed, err := modelImage(source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) > maxModelImageBytes {
		t.Fatalf("model image is %d bytes, limit %d", len(compressed), maxModelImageBytes)
	}
	if _, _, err := image.Decode(bytes.NewReader(compressed)); err != nil {
		t.Fatalf("bounded model image is unreadable: %v", err)
	}
}

func TestVerifyModelLive(t *testing.T) {
	if os.Getenv("SPYDER_LIVE_MODEL") != "1" {
		t.Skip("set SPYDER_LIVE_MODEL=1 for a live Claudia task")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	step := verify.Step{
		ID:        "live_model",
		Type:      verify.KindModel,
		Prompt:    "Return exactly PASS and nothing else.",
		Accept:    "PASS",
		ModelSpec: []byte(`{"mode":"task","purpose":"analysis","quality":"economy","prefer_provider":"claude","exclude_providers":["grok","codex","cursor","bedrock","ollama"]}`),
	}
	result := (&Handler{}).execVerifyModel(ctx, verify.StepRequest{Step: step, Cwd: t.TempDir(), Emit: func(line string) { t.Log(line) }})
	if result.Code != 0 || result.Output != "PASS" {
		t.Fatalf("live model: %+v", result)
	}
}

func TestVerifyModelScreenLive(t *testing.T) {
	if os.Getenv("SPYDER_LIVE_MODEL") != "1" {
		t.Skip("set SPYDER_LIVE_MODEL=1 for a live image-reading task")
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(&stubAdapter{screenshot: func(string) ([]byte, error) { return buf.Bytes(), nil }}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	shots := filepath.Join(t.TempDir(), "screenshots")
	step := verify.Step{
		ID:            "live_screen",
		Type:          verify.KindModel,
		Device:        "00008103-001122334455667A",
		CaptureScreen: true,
		Prompt:        "If the image is solid blue, return exactly PASS. Otherwise return FAIL.",
		Accept:        "PASS",
		ModelSpec:     []byte(`{"mode":"task","purpose":"analysis","quality":"economy","prefer_provider":"claude","exclude_providers":["grok","codex","cursor","bedrock","ollama"]}`),
	}
	result := h.execVerifyModel(ctx, verify.StepRequest{Step: step, Cwd: t.TempDir(), Env: map[string]string{"SPYDER_RUN_DIR": shots}, Emit: func(line string) { t.Log(line) }})
	if result.Code != 0 || result.Output != "PASS" {
		t.Fatalf("live screen model: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(shots, "live_screen.png")); err != nil {
		t.Fatalf("screenshot was not published: %v", err)
	}
}

func TestVerifyModelRejectsWrongScreenLive(t *testing.T) {
	path := os.Getenv("SPYDER_LIVE_WRONG_SCREEN_IMAGE")
	if path == "" {
		t.Skip("set SPYDER_LIVE_WRONG_SCREEN_IMAGE to a known non-Vehicles screen")
	}
	png, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(&stubAdapter{screenshot: func(string) ([]byte, error) { return png, nil }}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	step := verify.Step{
		ID:            "wrong_screen",
		Type:          verify.KindModel,
		Device:        "00008103-001122334455667A",
		CaptureScreen: true,
		Prompt:        "Return exactly PASS only if this is the Vehicles CARS shop showing Featured Car and class tiles. Otherwise return FAIL followed by a brief reason.",
		Accept:        "PASS",
		ModelSpec:     []byte(`{"mode":"task","purpose":"analysis","quality":"standard","prefer_provider":"claude","exclude_providers":["grok","codex","cursor","bedrock","ollama"]}`),
	}
	shotDir := filepath.Join(t.TempDir(), "screenshots")
	result := h.execVerifyModel(ctx, verify.StepRequest{Step: step, Cwd: t.TempDir(), Env: map[string]string{"SPYDER_RUN_DIR": shotDir}, Emit: func(line string) { t.Log(line) }})
	if result.Code == 0 || !strings.Contains(result.Output, "FAIL") {
		t.Fatalf("wrong screen was not rejected by model: %+v", result)
	}
	modelFile := filepath.Join(filepath.Dir(shotDir), "model-inputs", "wrong_screen.jpg")
	info, err := os.Stat(modelFile)
	if err != nil || info.Size() > maxModelImageBytes {
		t.Fatalf("model image exceeds stream budget: err=%v info=%v", err, info)
	}
}

func TestVerifyModelAcceptsNightScreenLive(t *testing.T) {
	path := os.Getenv("SPYDER_LIVE_NIGHT_SCREEN_IMAGE")
	if path == "" {
		t.Skip("set SPYDER_LIVE_NIGHT_SCREEN_IMAGE to a known night race screenshot")
	}
	pngData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(&stubAdapter{screenshot: func(string) ([]byte, error) { return pngData, nil }}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	step := verify.Step{
		ID:            "night_screen",
		Type:          verify.KindModel,
		Device:        "00008103-001122334455667A",
		CaptureScreen: true,
		Prompt:        "Inspect whether this fresh device image is the right starting screen for the owner check. Expected: A live Thunderdome 100 Night race is on screen, rather than a menu or another track. Return exactly PASS if it matches. Otherwise return FAIL followed by a short reason. Do not judge the owner's subjective question.",
		Accept:        "PASS",
		ModelSpec:     []byte(`{"mode":"task","purpose":"analysis","quality":"standard","prefer_provider":"claude","exclude_providers":["grok","codex","cursor","bedrock","ollama"]}`),
	}
	shotDir := filepath.Join(t.TempDir(), "screenshots")
	result := h.execVerifyModel(ctx, verify.StepRequest{Step: step, Cwd: t.TempDir(), Env: map[string]string{"SPYDER_RUN_DIR": shotDir}, Emit: func(line string) { t.Log(line) }})
	if result.Code != 0 || result.Output != "PASS" {
		t.Fatalf("matching night screen not accepted: %+v", result)
	}
}

func TestVerifyPhysicalScreenGateLive(t *testing.T) {
	device := os.Getenv("SPYDER_LIVE_VERIFY_DEVICE")
	if device == "" {
		t.Skip("set SPYDER_LIVE_VERIFY_DEVICE to a device showing a non-blue screen")
	}
	wf, err := verify.Load([]byte(`
name: physical-screen-review
steps:
  - id: review
    type: model
    device: ${device}
    capture_screen: true
    timeout_sec: 120
    model:
      mode: task
      purpose: analysis
      quality: economy
      prefer_provider: claude
      exclude_providers: [grok, codex, cursor, bedrock, ollama]
    prompt: Return exactly PASS only if this is a completely uniform blue square with no text or controls. Otherwise return FAIL.
    accept: PASS
  - id: owner
    type: human_gate
    requires: [review]
    prompt: Check the screen
    choices:
      - id: pass
        label: Pass
cleanup:
  - id: stop
    type: shell
    argv: [/usr/bin/true]
`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	h := NewHandler()
	run, err := h.VerifyHub().Start(ctx, verify.StartArgs{Workflow: wf, Cwd: t.TempDir(), Params: map[string]string{"device": device}})
	if err != nil {
		t.Fatal(err)
	}
	result := run.Wait()
	if result.Status == verify.StatusPassed || result.FailedStepID != "review" {
		t.Fatalf("wrong physical screen reached owner gate: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(result.ReportDir, "artifacts", "screenshots", "review.png")); err != nil {
		t.Fatalf("physical screenshot missing: %v", err)
	}
	events, err := os.ReadFile(filepath.Join(result.ReportDir, "events.log"))
	if err != nil || !strings.Contains(string(events), "model result: FAIL") {
		t.Fatalf("model did not classify physical screen: err=%v events=%q", err, events)
	}
	for _, step := range result.Steps {
		if step.StepID == "owner" {
			t.Fatalf("owner gate opened after rejection: %+v", result.Steps)
		}
	}
	t.Logf("physical screen rejected; report=%s reason=%s", result.ReportDir, result.FailedReason)
}
