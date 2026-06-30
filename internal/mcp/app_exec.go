// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// app_exec is spyder's scripting entry point (🎯T88). It runs an
// agent-supplied Starlark script server-side, exposing every spyder verb
// as a builtin so an agent can express ordered, timed, looping device
// action in one tool call — eliminating the per-action LLM round-trip
// that lets transient UI states vanish before capture.
//
// The engine (runExec) is deliberately decoupled from *Handler: it takes
// a verb map and limits, so it can be unit-tested against stub verbs with
// no devices. handleAppExec wires it to the live verb table.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// toolFunc is the shared signature of every spyder verb handler. The same
// map drives both MCP dispatch (dispatch) and the Starlark builtin bridge
// (runExec), so the two surfaces can never drift.
type toolFunc func(args map[string]any) (*mcpgo.CallToolResult, error)

const (
	// defaultExecSteps bounds Starlark interpreter steps so a runaway
	// loop cannot wedge the agent's single in-flight tool slot. Generous:
	// real action scripts use a tiny fraction of this.
	defaultExecSteps uint64 = 50_000_000
	// defaultExecDuration is the wall-clock budget when the caller omits
	// max_duration_ms. maxExecDuration is the hard ceiling.
	defaultExecDuration = 30 * time.Second
	maxExecDuration     = 120 * time.Second
	// maxSleepMillis caps a single sleep() so a script can't park on one
	// call past the duration budget; sleep also stops at the deadline.
	maxSleepMillis = int64(maxExecDuration / time.Millisecond)
)

// execLimits are the liveness caps applied to one app_exec run. They exist
// to protect the agent's tool slot, not the (disposable test) devices.
type execLimits struct {
	MaxSteps    uint64
	MaxDuration time.Duration
}

// handleAppExec is the MCP entry point. It does NOT hold h.mu: builtins
// call verb handlers that take h.mu themselves, so holding it here would
// deadlock. The script runs single-threaded, so the verb calls serialise
// naturally in script order.
func (h *Handler) handleAppExec(args map[string]any) (*mcpgo.CallToolResult, error) {
	script, err := requireString(args, "script")
	if err != nil {
		return nil, err
	}

	dur := defaultExecDuration
	if ms, ok := args["max_duration_ms"].(float64); ok && ms > 0 {
		dur = min(time.Duration(ms)*time.Millisecond, maxExecDuration)
	}

	verbs := h.toolHandlers()
	// app_exec is not itself a builtin — no nested scripting.
	delete(verbs, "app_exec")

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	return runExec(ctx, script, verbs, execLimits{MaxSteps: defaultExecSteps, MaxDuration: dur})
}

// runExec compiles and runs a Starlark script with verbs exposed as
// builtins, returning the ordered emitted artifacts as MCP content. A
// runtime error (including a cap breach) is reported as IsError with
// whatever was emitted before the failure preserved — never a hang.
func runExec(ctx context.Context, script string, verbs map[string]toolFunc, lim execLimits) (*mcpgo.CallToolResult, error) {
	st := &execState{ctx: ctx}

	predeclared := st.builtins(verbs)
	prog, err := compileExec(script, predeclared)
	if err != nil {
		// Compile/parse errors carry their own position.
		return mcpgo.NewToolResultError(fmt.Sprintf("app_exec: %v", err)), nil
	}

	thread := &starlark.Thread{Name: "app_exec"}
	thread.SetMaxExecutionSteps(lim.MaxSteps)

	// Wall-clock guard: cancel the interpreter if it overruns. Stopped as
	// soon as Init returns so the timer never fires on the fast path.
	timer := time.AfterFunc(lim.MaxDuration, func() {
		thread.Cancel("max_duration exceeded")
	})

	_, runErr := prog.Init(thread, predeclared)
	timer.Stop()

	content := st.content()
	if runErr != nil {
		msg := runErr.Error()
		if evalErr, ok := runErr.(*starlark.EvalError); ok {
			msg = evalErr.Backtrace()
		}
		content = append(content, mcpgo.NewTextContent("app_exec error: "+msg))
		return &mcpgo.CallToolResult{Content: content, IsError: true}, nil
	}
	if len(content) == 0 {
		content = append(content, mcpgo.NewTextContent("(app_exec produced no output)"))
	}
	return &mcpgo.CallToolResult{Content: content}, nil
}

// execState accumulates the ordered output buffer across one run.
type execState struct {
	ctx context.Context
	out []starlark.Value
}

// builtins constructs the predeclared environment: one bridge builtin per
// verb, plus emit/sleep/help.
func (st *execState) builtins(verbs map[string]toolFunc) starlark.StringDict {
	g := make(starlark.StringDict, len(verbs)+3)
	for name, fn := range verbs {
		g[name] = st.verbBuiltin(name, fn)
	}
	g["emit"] = starlark.NewBuiltin("emit", st.emit)
	g["sleep"] = starlark.NewBuiltin("sleep", st.sleep)
	g["help"] = starlark.NewBuiltin("help", helpBuiltin(verbs))
	return g
}

// verbBuiltin bridges one spyder verb to a Starlark builtin: keyword args
// become the verb's argument map, the verb runs, and its CallToolResult is
// mapped back to a Starlark value (images returned as opaque image values;
// JSON text decoded to dict/list; plain text as a string). A verb that
// reports IsError surfaces as a Starlark error carrying the script call
// position.
func (st *execState) verbBuiltin(name string, fn toolFunc) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) > 0 {
			return nil, fmt.Errorf("%s: pass arguments by keyword, e.g. %s(device=\"iPad\")", name, name)
		}
		argMap := make(map[string]any, len(kwargs))
		for _, kv := range kwargs {
			key, ok := starlark.AsString(kv[0])
			if !ok {
				return nil, fmt.Errorf("%s: non-string argument name %v", name, kv[0])
			}
			gv, err := starlarkToGo(kv[1])
			if err != nil {
				return nil, fmt.Errorf("%s: argument %q: %w", name, key, err)
			}
			argMap[key] = gv
		}
		res, err := fn(argMap)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return resultToStarlark(name, res)
	})
}

// emit appends a value to the output buffer in order. None is ignored, so
// a bare verb call with no meaningful return (e.g. app_input) does not add
// a null block, and the top-level-expression auto-emit (see compileExec)
// is a no-op for such calls.
func (st *execState) emit(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("emit: takes positional values only")
	}
	for _, v := range args {
		if v == starlark.None {
			continue
		}
		st.out = append(st.out, v)
	}
	return starlark.None, nil
}

// sleep pauses the script for ms milliseconds, clamped to the run's
// remaining wall-clock budget and interruptible by cancellation.
func (st *execState) sleep(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var ms int64
	if err := starlark.UnpackArgs("sleep", args, kwargs, "ms", &ms); err != nil {
		return nil, err
	}
	if ms < 0 {
		return nil, fmt.Errorf("sleep: ms must be >= 0")
	}
	if ms > maxSleepMillis {
		ms = maxSleepMillis
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return starlark.None, nil
	case <-st.ctx.Done():
		return nil, fmt.Errorf("sleep: %w", st.ctx.Err())
	}
}

// content renders the ordered output buffer to MCP content blocks.
func (st *execState) content() []mcpgo.Content {
	out := make([]mcpgo.Content, 0, len(st.out))
	for _, v := range st.out {
		out = append(out, valueToContent(v))
	}
	return out
}

// helpBuiltin returns a builtin that lists the available verbs, so an
// agent can discover the API from within a script (there are no per-tool
// MCP schemas once app_exec is the sole entry point).
func helpBuiltin(verbs map[string]toolFunc) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	names := make([]string, 0, len(verbs))
	for n := range verbs {
		names = append(names, n)
	}
	sort.Strings(names)
	text := "verbs: " + strings.Join(names, ", ") +
		"\ncontrol: emit(value), sleep(ms)\n" +
		"call verbs by keyword, e.g. app_screenshot(session_id=\"...\"); " +
		"a bare expression or emit() adds to the result."
	return func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		return starlark.String(text), nil
	}
}

// imageValue is an opaque Starlark value wrapping a captured image. It
// carries the base64 payload straight through to an MCP image block,
// avoiding a decode/re-encode round-trip.
type imageValue struct {
	mime string
	b64  string
}

func (imageValue) Type() string          { return "image" }
func (imageValue) Freeze()                {}
func (imageValue) Truth() starlark.Bool   { return starlark.True }
func (i imageValue) String() string       { return fmt.Sprintf("<image %s, %d b64 bytes>", i.mime, len(i.b64)) }
func (imageValue) Hash() (uint32, error)  { return 0, fmt.Errorf("image is unhashable") }

// compileExec parses the script and compiles it. REPL-style, only the
// FINAL top-level expression statement is rewritten into emit(<expr>), so
// a one-liner like `app_screenshot(...)` returns its value while
// intermediate bare action calls (whose ack strings would be noise) are
// discarded like ordinary statements. Explicit emit() produces output
// anywhere. emit ignores None, so double-wrapping a trailing emit(...) is
// harmless.
func compileExec(script string, predeclared starlark.StringDict) (*starlark.Program, error) {
	// TopLevelControl allows top-level for/if (e.g. a bounded poll loop);
	// GlobalReassign lets a script reassign a top-level variable. while and
	// recursion stay disabled — bounded iteration is a safety property, and
	// the step budget backstops the rest.
	opts := &syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}
	file, err := opts.Parse("script.star", script, 0)
	if err != nil {
		return nil, err
	}
	if n := len(file.Stmts); n > 0 {
		if es, ok := file.Stmts[n-1].(*syntax.ExprStmt); ok {
			start, _ := es.X.Span()
			es.X = &syntax.CallExpr{
				Fn:     &syntax.Ident{NamePos: start, Name: "emit"},
				Lparen: start,
				Args:   []syntax.Expr{es.X},
				Rparen: start,
			}
		}
	}
	isPredeclared := func(name string) bool { _, ok := predeclared[name]; return ok }
	return starlark.FileProgram(file, isPredeclared)
}

// starlarkToGo converts a Starlark argument value into the any-typed value
// the verb handlers expect in their argument map.
func starlarkToGo(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(t), nil
	case starlark.String:
		return string(t), nil
	case starlark.Int:
		i, _ := t.Int64()
		return i, nil
	case starlark.Float:
		return float64(t), nil
	case *starlark.List:
		out := make([]any, 0, t.Len())
		it := t.Iterate()
		defer it.Done()
		var e starlark.Value
		for it.Next(&e) {
			g, err := starlarkToGo(e)
			if err != nil {
				return nil, err
			}
			out = append(out, g)
		}
		return out, nil
	case starlark.Tuple:
		out := make([]any, 0, len(t))
		for _, e := range t {
			g, err := starlarkToGo(e)
			if err != nil {
				return nil, err
			}
			out = append(out, g)
		}
		return out, nil
	case *starlark.Dict:
		out := make(map[string]any, t.Len())
		for _, item := range t.Items() {
			key, ok := starlark.AsString(item[0])
			if !ok {
				return nil, fmt.Errorf("dict key %v is not a string", item[0])
			}
			g, err := starlarkToGo(item[1])
			if err != nil {
				return nil, err
			}
			out[key] = g
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported argument type %s", v.Type())
	}
}

// goToStarlark converts a decoded JSON value (from a verb's text result)
// into a Starlark value.
func goToStarlark(v any) starlark.Value {
	switch t := v.(type) {
	case nil:
		return starlark.None
	case bool:
		return starlark.Bool(t)
	case float64:
		// JSON numbers decode as float64; present integers as ints.
		if t == float64(int64(t)) {
			return starlark.MakeInt64(int64(t))
		}
		return starlark.Float(t)
	case string:
		return starlark.String(t)
	case []any:
		elems := make([]starlark.Value, len(t))
		for i, e := range t {
			elems[i] = goToStarlark(e)
		}
		return starlark.NewList(elems)
	case map[string]any:
		d := starlark.NewDict(len(t))
		// Sorted keys for deterministic iteration.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_ = d.SetKey(starlark.String(k), goToStarlark(t[k]))
		}
		return d
	default:
		return starlark.String(fmt.Sprintf("%v", t))
	}
}

// resultToStarlark maps a verb's CallToolResult to the Starlark value the
// script sees. Image content becomes an opaque image value; a single JSON
// text block decodes to dict/list; other text becomes a string. An error
// result becomes a Starlark error (raised at the call site).
func resultToStarlark(name string, res *mcpgo.CallToolResult) (starlark.Value, error) {
	if res == nil {
		return starlark.None, nil
	}
	if res.IsError {
		return nil, fmt.Errorf("%s: %s", name, contentText(res.Content))
	}
	var texts []string
	for _, c := range res.Content {
		switch cc := c.(type) {
		case mcpgo.ImageContent:
			return imageValue{mime: cc.MIMEType, b64: cc.Data}, nil
		case mcpgo.TextContent:
			texts = append(texts, cc.Text)
		}
	}
	switch len(texts) {
	case 0:
		return starlark.None, nil
	case 1:
		var decoded any
		if err := json.Unmarshal([]byte(texts[0]), &decoded); err == nil {
			switch decoded.(type) {
			case map[string]any, []any:
				return goToStarlark(decoded), nil
			}
		}
		return starlark.String(texts[0]), nil
	default:
		elems := make([]starlark.Value, len(texts))
		for i, t := range texts {
			elems[i] = starlark.String(t)
		}
		return starlark.NewList(elems), nil
	}
}

// valueToContent renders one emitted Starlark value to an MCP content
// block: images as image blocks, strings as text, everything else as JSON.
func valueToContent(v starlark.Value) mcpgo.Content {
	switch t := v.(type) {
	case imageValue:
		return mcpgo.NewImageContent(t.b64, t.mime)
	case starlark.String:
		return mcpgo.NewTextContent(string(t))
	default:
		g, err := starlarkToGo(v)
		if err != nil {
			return mcpgo.NewTextContent(v.String())
		}
		data, err := json.Marshal(g)
		if err != nil {
			return mcpgo.NewTextContent(v.String())
		}
		return mcpgo.NewTextContent(string(data))
	}
}

// contentText flattens the text blocks of a result for error reporting.
func contentText(content []mcpgo.Content) string {
	var b strings.Builder
	for _, c := range content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tc.Text)
		}
	}
	if b.Len() == 0 {
		return "error"
	}
	return b.String()
}

// appExecDefinition is the MCP tool schema for app_exec. The description
// teaches the surface because, as app_exec becomes the sole entry point
// (🎯T88), there are no per-verb schemas to discover (full reference lives
// in agents-guide.md; call help() from a script for the verb list).
func appExecDefinition() mcpgo.Tool {
	return mcpgo.NewTool("app_exec",
		mcpgo.WithDescription("Run a Starlark script server-side with spyder's verbs as builtins — the way to drive ordered, timed, looping device action in ONE call without per-action agent round-trips (so transient UI states don't vanish between a tap and its screenshot).\n\nBuiltins: every spyder verb is a function called by keyword, e.g. `app_screenshot(session_id=\"s1\")`, `app_input(session_id=\"s1\", events=[...])`, `screenshot(device=\"iPad\")`, `app_pause(session_id=\"s1\")`, `app_step(session_id=\"s1\", frames=1)`. Plus `sleep(ms)` (wall-clock delay, ms-accurate), `emit(value)` (append a value to the result), and `help()` (list verbs).\n\nResult model: a bare top-level expression OR `emit(x)` appends to the ordered result — image values become image blocks, other values become JSON/text. So `app_screenshot(session_id=\"s1\")` on its own line returns the image; a verb with no useful return (e.g. app_input) adds nothing. All artifacts come back from the one call, in order.\n\nDeterministic capture: `app_pause` → `app_input` → `app_step(frames=1)` → `app_screenshot` freezes the exact frame regardless of jitter. Use a bounded `for _ in range(N): ... ; sleep(ms)` to poll. Caps: wall-clock timeout (default 30s, max 120s) and a step budget; on breach, whatever was emitted is returned with an error note."),
		mcpgo.WithString("script",
			mcpgo.Required(),
			mcpgo.Description("Starlark source. Call verbs by keyword; use emit()/bare expressions to produce output; sleep(ms) to pace; bounded for-loops to repeat (no while/recursion)."),
		),
		mcpgo.WithNumber("max_duration_ms",
			mcpgo.Description("Wall-clock budget for the whole script in milliseconds (default 30000, capped at 120000)."),
		),
	)
}
