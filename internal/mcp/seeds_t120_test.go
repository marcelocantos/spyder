// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/scriptlib"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// 🎯T120: the bundled deploy/mode-smoke seeds are listed by the durable
// script library, and every bundled recipe compiles against the real
// builtin surface (no phantom verbs, no syntax errors) — runnable as-is.
func TestBundledSeeds_ListedAndCompile(t *testing.T) {
	h := NewHandler()
	verbs := h.toolHandlers()
	delete(verbs, "app_exec") // mirrors handleAppExec: no nested scripting
	st := &execState{}
	predeclared := st.builtins(verbs, nil)

	list, err := scriptlib.List()
	if err != nil {
		t.Fatal(err)
	}
	seeds := map[string]bool{
		"deploy_and_sid": false,
		"yw_mode_smoke":  false,
	}
	for _, s := range list {
		if s.Source != "bundled" {
			continue
		}
		src, _, err := scriptlib.Load("bundled:" + s.Name)
		if err != nil {
			t.Errorf("load bundled %s: %v", s.Name, err)
			continue
		}
		if _, err := compileExec(src, predeclared); err != nil {
			t.Errorf("bundled recipe %s does not compile: %v", s.Name, err)
		}
		if _, ok := seeds[s.Name]; ok {
			seeds[s.Name] = true
		}
	}
	for name, seen := range seeds {
		if !seen {
			t.Errorf("🎯T120 seed %q not listed by scriptlib.List", name)
		}
	}
}

// The deploy_and_sid seed runs end-to-end against stub verbs: deploy,
// poll the channel listing, emit the session id. Missing params fail
// closed with a clear message.
func TestSeed_DeployAndSid_Runtime(t *testing.T) {
	src, _, err := scriptlib.Load("bundled:deploy_and_sid")
	if err != nil {
		t.Fatal(err)
	}
	verbs := map[string]toolFunc{
		"deploy_app": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(`{"bundle_id":"com.squz.yourworld","pid":123}`), nil
		},
		"app_channel_list": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(`{"listeners":[{"bundle_id":"com.squz.yourworld","port":54321,"sessions":[{"session_id":"s9"}]}]}`), nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := runExec(ctx, src, verbs, health.New(), nil, defaultLim(),
		map[string]string{"device": "Jevons", "path": "/tmp/App.app"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("seed errored: %v", texts(res))
	}
	joined := strings.Join(texts(res), "\n")
	for _, want := range []string{"s9", "com.squz.yourworld", "54321", "deploy_and_sid"} {
		if !strings.Contains(joined, want) {
			t.Errorf("output missing %q: %s", want, joined)
		}
	}

	// Fail closed without params.
	res, err = runExec(ctx, src, verbs, health.New(), nil, defaultLim(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("seed should fail without device/path params")
	}
	if joined := strings.Join(texts(res), "\n"); !strings.Contains(joined, "device and path are required") {
		t.Errorf("missing-params diagnostic: %s", joined)
	}
}

// The yw_mode_smoke seed discovers app RPCs before invoking them, and
// fails closed when the game doesn't advertise start_mode.
func TestSeed_YwModeSmoke_Runtime(t *testing.T) {
	src, _, err := scriptlib.Load("bundled:yw_mode_smoke")
	if err != nil {
		t.Fatal(err)
	}
	verbs := map[string]toolFunc{
		"app_methods": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(`{"app_name":"yourworld","methods":[{"name":"start_mode"},{"name":"set_view"}]}`), nil
		},
		"app_call": func(a map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(`{"ok":true}`), nil
		},
		"app_state": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(`{"mode":"Europe","started":true}`), nil
		},
		"app_screenshot": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultImage("capture", "QUJD", "image/png"), nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := runExec(ctx, src, verbs, health.New(), nil,
		execLimits{MaxSteps: defaultExecSteps, MaxDuration: 10 * time.Second},
		map[string]string{"session_id": "s1", "mode": "europe", "lon": "31.5", "lat": "33.8", "zoom": "2.25"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("seed errored: %v", texts(res))
	}
	joined := strings.Join(texts(res), "\n")
	for _, want := range []string{"yw_mode_smoke", "europe", "Europe"} {
		if !strings.Contains(joined, want) {
			t.Errorf("output missing %q: %s", want, joined)
		}
	}
	if len(images(res)) != 1 {
		t.Errorf("want 1 screenshot image block, got %d", len(images(res)))
	}

	// Fail closed when start_mode is not advertised.
	verbs["app_methods"] = func(map[string]any) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText(`{"app_name":"other","methods":[{"name":"ping"}]}`), nil
	}
	res, err = runExec(ctx, src, verbs, health.New(), nil, defaultLim(),
		map[string]string{"session_id": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("seed should fail when start_mode is not advertised")
	}
	if joined := strings.Join(texts(res), "\n"); !strings.Contains(joined, "does not advertise start_mode") {
		t.Errorf("fail-closed diagnostic: %s", joined)
	}
}
