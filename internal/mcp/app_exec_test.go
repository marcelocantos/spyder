// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// stubVerbs returns a self-contained verb table for engine tests — no
// devices, no sessions. The recording map is shared across calls so a
// handle round-trip can be exercised.
func stubVerbs() map[string]toolFunc {
	rec := map[string]bool{}
	return map[string]toolFunc{
		"say_text": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("hello"), nil
		},
		"say_json": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(`{"x":42,"name":"abc"}`), nil
		},
		"shot": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultImage("capture", "QUJD", "image/png"), nil
		},
		"noop_ack": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		},
		"boom": func(map[string]any) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultError("kaboom"), nil
		},
		"echo": func(a map[string]any) (*mcpgo.CallToolResult, error) {
			b, _ := json.Marshal(a)
			return mcpgo.NewToolResultText(string(b)), nil
		},
		"rec_start": func(map[string]any) (*mcpgo.CallToolResult, error) {
			rec["rec-1"] = true
			return mcpgo.NewToolResultText(`{"id":"rec-1"}`), nil
		},
		"rec_stop": func(a map[string]any) (*mcpgo.CallToolResult, error) {
			id, _ := a["id"].(string)
			if !rec[id] {
				return mcpgo.NewToolResultError("no such recording: " + id), nil
			}
			delete(rec, id)
			return mcpgo.NewToolResultText("stopped"), nil
		},
	}
}

func runScript(t *testing.T, script string, verbs map[string]toolFunc, lim execLimits) *mcpgo.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lim.MaxDuration)
	defer cancel()
	res, err := runExec(ctx, script, verbs, health.New(), nil, lim)
	if err != nil {
		t.Fatalf("runExec returned a transport error (should be IsError instead): %v", err)
	}
	if res == nil {
		t.Fatal("runExec returned nil result")
	}
	return res
}

func defaultLim() execLimits {
	return execLimits{MaxSteps: defaultExecSteps, MaxDuration: 5 * time.Second}
}

func texts(res *mcpgo.CallToolResult) []string {
	var out []string
	for _, c := range res.Content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			out = append(out, tc.Text)
		}
	}
	return out
}

func images(res *mcpgo.CallToolResult) []mcpgo.ImageContent {
	var out []mcpgo.ImageContent
	for _, c := range res.Content {
		if ic, ok := c.(mcpgo.ImageContent); ok {
			out = append(out, ic)
		}
	}
	return out
}

// Content mapping and ordering: text -> string block, JSON -> JSON block,
// image -> image block, in emission order.
func TestExec_ContentMappingAndOrder(t *testing.T) {
	res := runScript(t, `
emit(say_text())
emit(say_json())
emit(shot())
`, stubVerbs(), defaultLim())

	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	if len(res.Content) != 3 {
		t.Fatalf("want 3 content blocks, got %d: %+v", len(res.Content), res.Content)
	}
	if _, ok := res.Content[0].(mcpgo.TextContent); !ok {
		t.Errorf("block 0: want text, got %T", res.Content[0])
	}
	if res.Content[0].(mcpgo.TextContent).Text != "hello" {
		t.Errorf("block 0 text = %q", res.Content[0].(mcpgo.TextContent).Text)
	}
	// JSON object decodes and re-renders with sorted keys.
	if jt := res.Content[1].(mcpgo.TextContent).Text; !strings.Contains(jt, `"name":"abc"`) || !strings.Contains(jt, `"x":42`) {
		t.Errorf("block 1 json = %q", jt)
	}
	img, ok := res.Content[2].(mcpgo.ImageContent)
	if !ok {
		t.Fatalf("block 2: want image, got %T", res.Content[2])
	}
	if img.Data != "QUJD" || img.MIMEType != "image/png" {
		t.Errorf("image = %+v", img)
	}
}

// A one-line bare expression returns its value (cheap as the old tool).
func TestExec_BareLastExpressionEmits(t *testing.T) {
	res := runScript(t, `shot()`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	if len(images(res)) != 1 {
		t.Fatalf("want 1 image from bare one-liner, got content %+v", res.Content)
	}
}

// Intermediate bare action calls are discarded (no ack-string noise); only
// the trailing expression and explicit emit()s produce output.
func TestExec_IntermediateBareCallsDiscarded(t *testing.T) {
	res := runScript(t, `
noop_ack()
sleep(1)
shot()
`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	if len(res.Content) != 1 || len(images(res)) != 1 {
		t.Fatalf("want exactly 1 image and no ack text, got %+v", res.Content)
	}
}

// emit(None) is a no-op; double-wrapping a trailing emit() is harmless.
func TestExec_EmitNoneIgnored(t *testing.T) {
	res := runScript(t, `
emit(None)
emit(say_text())
`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	if got := texts(res); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("want exactly [hello], got %v", got)
	}
}

// JSON results decode to indexable Starlark values.
func TestExec_JSONDecodeAndIndex(t *testing.T) {
	res := runScript(t, `
d = say_json()
emit(d["name"])
`, stubVerbs(), defaultLim())
	if got := texts(res); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("want [abc], got %v (isError=%v)", got, res.IsError)
	}
}

// Durable handles round-trip across separate app_exec calls: start in one,
// stop in the next, against the shared registry.
func TestExec_HandleRoundTripAcrossCalls(t *testing.T) {
	verbs := stubVerbs()

	start := runScript(t, `h = rec_start(); emit(h["id"])`, verbs, defaultLim())
	if got := texts(start); len(got) != 1 || got[0] != "rec-1" {
		t.Fatalf("start: want handle rec-1, got %v", got)
	}

	stop := runScript(t, `rec_stop(id="rec-1")`, verbs, defaultLim())
	if stop.IsError {
		t.Fatalf("stop: unexpected error: %v", texts(stop))
	}
	if got := texts(stop); len(got) != 1 || got[0] != "stopped" {
		t.Fatalf("stop: want [stopped], got %v", got)
	}

	// Stopping again fails — the registry no longer holds it.
	again := runScript(t, `rec_stop(id="rec-1")`, verbs, defaultLim())
	if !again.IsError {
		t.Fatalf("second stop should error, got %v", texts(again))
	}
}

// A verb error surfaces as an IsError result citing the verb.
func TestExec_VerbErrorIsError(t *testing.T) {
	res := runScript(t, `boom()`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("want IsError for a verb that returns an error")
	}
	if msg := strings.Join(texts(res), "\n"); !strings.Contains(msg, "boom") || !strings.Contains(msg, "kaboom") {
		t.Errorf("error message should cite the verb and reason, got %q", msg)
	}
}

// Positional arguments are rejected — verbs take keyword args.
func TestExec_PositionalArgsRejected(t *testing.T) {
	res := runScript(t, `say_text("oops")`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("want IsError for positional args")
	}
	if msg := strings.Join(texts(res), "\n"); !strings.Contains(msg, "keyword") {
		t.Errorf("want a keyword-args hint, got %q", msg)
	}
}

// Keyword args reach the verb as its argument map.
func TestExec_KeywordArgsReachVerb(t *testing.T) {
	res := runScript(t, `echo(device="iPad", n=3, on=True, tags=["a","b"])`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	got := texts(res)[0]
	for _, want := range []string{`"device":"iPad"`, `"n":3`, `"on":true`, `"tags":["a","b"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("echoed args %q missing %q", got, want)
		}
	}
}

// The step budget fires on a runaway loop, and output emitted before the
// breach is preserved.
func TestExec_StepBudgetCapWithPartialOutput(t *testing.T) {
	lim := execLimits{MaxSteps: 100_000, MaxDuration: 5 * time.Second}
	res := runScript(t, `
emit(say_text())
for i in range(1000000000):
    x = i
`, stubVerbs(), lim)
	if !res.IsError {
		t.Fatal("want IsError when the step budget is exceeded")
	}
	if got := texts(res); len(got) == 0 || got[0] != "hello" {
		t.Errorf("partial output before the breach should be preserved, got %v", got)
	}
}

// The wall-clock cap fires on an over-long sleep, preserving partial output.
func TestExec_DurationCapWithPartialOutput(t *testing.T) {
	lim := execLimits{MaxSteps: defaultExecSteps, MaxDuration: 60 * time.Millisecond}
	res := runScript(t, `
emit(say_text())
sleep(60000)
emit(shot())
`, stubVerbs(), lim)
	if !res.IsError {
		t.Fatal("want IsError when the duration budget is exceeded")
	}
	if got := texts(res); len(got) == 0 || got[0] != "hello" {
		t.Errorf("partial output before the cap should be preserved, got %v", got)
	}
	if len(images(res)) != 0 {
		t.Error("the post-sleep screenshot should not have run")
	}
}

// Sandbox: load() statements and undefined builtins are rejected — no host
// access leaks in.
func TestExec_SandboxBlocksLoadAndUnknownNames(t *testing.T) {
	load := runScript(t, `load("evil.star", "x")`, stubVerbs(), defaultLim())
	if !load.IsError {
		t.Error("load() should be rejected")
	}
	open := runScript(t, `open("/etc/passwd")`, stubVerbs(), defaultLim())
	if !open.IsError {
		t.Error("an undefined builtin like open() should be rejected")
	}
}

// Every verb spyder ever advertised as a one-off tool is reachable as an
// app_exec builtin, and there are no orphan builtins. This is the parity
// guarantee for the single-entry-point migration (🎯T88.2): it uses the
// definition-builder functions (which outlive the T88.3 removal of the
// tools from Definitions()), not Definitions() itself.
func TestExec_BuiltinCoversEveryAdvertisedVerb(t *testing.T) {
	h := &Handler{}
	builtins := h.toolHandlers()

	legacy := legacyDefinitions()
	legacyNames := make(map[string]bool, len(legacy))
	for _, tool := range legacy {
		legacyNames[tool.Name] = true
		if _, ok := builtins[tool.Name]; !ok {
			t.Errorf("verb %q is advertised but has no app_exec builtin", tool.Name)
		}
	}
	for name := range builtins {
		if name == "app_exec" {
			continue
		}
		if !legacyNames[name] {
			t.Errorf("builtin %q has no advertised definition (orphan)", name)
		}
	}
}

// The advertised MCP surface is app_exec alone (🎯T88.3) — the daemon
// registers exactly Definitions(), so this is what clients can call.
func TestDefinitions_SingleEntryPoint(t *testing.T) {
	defs := Definitions()
	if len(defs) != 1 || defs[0].Name != "app_exec" {
		names := make([]string, len(defs))
		for i, d := range defs {
			names[i] = d.Name
		}
		t.Fatalf("Definitions() must expose only app_exec, got %v", names)
	}
}

// help() lists the available verbs from within a script.
func TestExec_HelpListsVerbs(t *testing.T) {
	res := runScript(t, `help()`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	got := texts(res)[0]
	if !strings.Contains(got, "say_text") || !strings.Contains(got, "emit(") {
		t.Errorf("help text should list verbs and controls, got %q", got)
	}
}
