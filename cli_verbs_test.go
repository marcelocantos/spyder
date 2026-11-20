// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// cliSourceVerbs parses this package's non-test source and returns, for
// each hand-written command, the verbs its run function reaches (through
// any same-package helpers), plus every verb literal passed to a daemon
// call anywhere.
func cliSourceVerbs(t *testing.T) (byCommand map[string]map[string]bool, all map[string]bool) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	funcs := map[string]*ast.FuncDecl{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				funcs[fd.Name.Name] = fd
			}
		}
	}
	// The verb argument position of each daemon-call helper.
	verbArg := map[string]int{"postTool": 1, "dispatchAndExit": 1, "fanOutPhysical": 2}
	all = map[string]bool{}
	var reach func(name string, seen map[string]bool, out map[string]bool)
	reach = func(name string, seen map[string]bool, out map[string]bool) {
		fd := funcs[name]
		if fd == nil || seen[name] {
			return
		}
		seen[name] = true
		ast.Inspect(fd, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if pos, ok := verbArg[id.Name]; ok {
				if pos < len(call.Args) {
					if b, ok := call.Args[pos].(*ast.BasicLit); ok && b.Kind == token.STRING {
						s, _ := strconv.Unquote(b.Value)
						out[s] = true
					}
				}
				return true
			}
			reach(id.Name, seen, out)
			return true
		})
	}
	for name := range funcs {
		reach(name, map[string]bool{}, all)
	}
	byCommand = map[string]map[string]bool{}
	for _, c := range cliCommands {
		fn := funcNameOf(t, c)
		out := map[string]bool{}
		reach(fn, map[string]bool{}, out)
		byCommand[c.name] = out
	}
	return byCommand, all
}

// funcNameOf finds the run function's name for a cliCommands entry by
// matching the registration in source.
func funcNameOf(t *testing.T, c cliCommand) string {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var found string
		ast.Inspect(af, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || len(cl.Elts) != 3 {
				return true
			}
			b, ok1 := cl.Elts[0].(*ast.BasicLit)
			id, ok2 := cl.Elts[2].(*ast.Ident)
			if ok1 && ok2 && b.Kind == token.STRING {
				if s, _ := strconv.Unquote(b.Value); s == c.name {
					found = id.Name
				}
			}
			return true
		})
		if found != "" {
			return found
		}
	}
	t.Fatalf("no registration found for command %q", c.name)
	return ""
}

// No CLI code calls a verb the daemon does not have.
func TestCLICallsOnlyRealVerbs(t *testing.T) {
	_, all := cliSourceVerbs(t)
	for v := range all {
		if lookupVerb(v) == nil {
			t.Errorf("CLI calls verb %q, which is not in the daemon's verb table", v)
		}
	}
}

// cliVerbOf names exactly the hand-written commands that call one verb,
// and names the right verb, so each takes all of its verb's parameters.
func TestCLIVerbMapMatchesSource(t *testing.T) {
	byCommand, _ := cliSourceVerbs(t)
	for cmd, verbs := range byCommand {
		var list []string
		for v := range verbs {
			list = append(list, v)
		}
		sort.Strings(list)
		mapped, ok := cliVerbOf[cmd]
		switch {
		case len(list) == 1 && !ok:
			t.Errorf("command %q calls only %s but is missing from cliVerbOf", cmd, list[0])
		case len(list) == 1 && mapped != list[0]:
			t.Errorf("cliVerbOf[%q] = %q, but the command calls %s", cmd, mapped, list[0])
		case len(list) != 1 && ok:
			t.Errorf("cliVerbOf[%q] = %q, but the command calls %v", cmd, mapped, list)
		}
	}
	for cmd := range cliVerbOf {
		if _, ok := byCommand[cmd]; !ok {
			t.Errorf("cliVerbOf names %q, which is not a CLI command", cmd)
		}
	}
}

// Every verb runs from the CLI under its own name, in both spellings,
// and no hand-written command takes a verb's name for something else.
func TestEveryVerbRunsFromCLI(t *testing.T) {
	byCommand, _ := cliSourceVerbs(t)
	for _, v := range verbSchemas() {
		for _, name := range []string{v.Name, strings.ReplaceAll(v.Name, "_", "-")} {
			if c := lookupCLI(name); c != nil {
				if !byCommand[name][v.Name] {
					t.Errorf("command %q shadows verb %s but does not call it", name, v.Name)
				}
				continue
			}
			if got := lookupVerb(name); got == nil || got.Name != v.Name {
				t.Errorf("`spyder %s` does not reach verb %s", name, v.Name)
			}
		}
	}
}

// A verb parameter never shares a name with a CLI flag, which would
// make it unreachable as a flag.
func TestVerbParamsDoNotCollideWithCLIFlags(t *testing.T) {
	for _, v := range verbSchemas() {
		for _, p := range v.Params {
			flag := "--" + strings.ReplaceAll(p.Name, "_", "-")
			if verbFlags[flag] || verbFlags["--"+p.Name] {
				t.Errorf("verb %s parameter %q collides with the CLI flag %s", v.Name, p.Name, flag)
			}
		}
	}
}

func TestParseVerbArgsTypes(t *testing.T) {
	v := lookupVerb("deploy_app")
	inv, err := parseVerbArgs(v, []string{
		"--device", "S24", "--path=/a/g.apk",
		"--parts", "/a/x.main.obb", "--parts", "/a/x.patch.obb",
		"--env", "A=1", "--env=B=2", "--as", "me",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"device": "S24", "path": "/a/g.apk",
		"parts": []any{"/a/x.main.obb", "/a/x.patch.obb"},
		"env":   map[string]any{"A": "1", "B": "2"},
		"owner": "me",
	}
	if !reflect.DeepEqual(inv.args, want) {
		t.Fatalf("args = %#v\nwant %#v", inv.args, want)
	}

	inv, err = parseVerbArgs(lookupVerb("app_exec"), []string{
		"--script", "emit(1)", "--max-duration-ms", "5000", "--params", `{"k":"v"}`, "--json",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inv.args["max_duration_ms"] != 5000.0 || !inv.json || !reflect.DeepEqual(inv.args["params"], map[string]any{"k": "v"}) {
		t.Fatalf("app_exec args = %#v json=%v", inv.args, inv.json)
	}

	inv, err = parseVerbArgs(lookupVerb("reserve"), []string{"--device", "S24", "--force", "--args", `{"ttl_seconds": 60, "device": "X"}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inv.args["force"] != true || inv.args["device"] != "S24" || inv.args["ttl_seconds"] != 60.0 {
		t.Fatalf("reserve args = %#v (flags must override --args)", inv.args)
	}
}

func TestParseVerbArgsErrors(t *testing.T) {
	v := lookupVerb("deploy_app")
	for _, argv := range [][]string{
		{"--device", "S24"}, // missing required path
		{"--device", "S24", "--path", "/a", "--nope", "1"}, // unknown flag
		{"S24", "/a"}, // positionals
		{"--device"},  // missing value
	} {
		if _, err := parseVerbArgs(v, argv, nil); err == nil {
			t.Errorf("parseVerbArgs(%v) accepted", argv)
		}
	}
	if _, err := parseVerbArgs(lookupVerb("reserve"), []string{"--force=maybe"}, nil); err == nil {
		t.Error("bad boolean accepted")
	}
	if _, err := parseVerbArgs(lookupVerb("app_exec"), []string{"--max-duration-ms", "soon"}, nil); err == nil {
		t.Error("bad number accepted")
	}
}

// A hand-written command passes the verb parameters it does not handle
// itself through to its verb.
func TestHandWrittenCommandPassesVerbParamsThrough(t *testing.T) {
	t.Cleanup(func() { passthrough.verb, passthrough.args = "", nil })
	own := map[string]bool{"--as": true, "--bundle-id": true, "--timeout": true}
	kept, err := extractPassthrough("deploy", []string{"S24", "/a/g.apk", "--env", "A=1", "--bundle-id", "x", "--parts=/a/m.obb"}, own)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"S24", "/a/g.apk", "--bundle-id", "x"}; !reflect.DeepEqual(kept, want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	got := mergePassthrough("deploy_app", map[string]any{"device": "S24", "path": "/a/g.apk", "parts": []any{"/b/explicit.obb"}})
	want := map[string]any{
		"device": "S24", "path": "/a/g.apk",
		"parts": []any{"/b/explicit.obb"}, // the command's own value wins
		"env":   map[string]any{"A": "1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged %#v\nwant %#v", got, want)
	}
	if other := mergePassthrough("devices", map[string]any{}); len(other) != 0 {
		t.Fatalf("passthrough leaked into another verb: %v", other)
	}
}
