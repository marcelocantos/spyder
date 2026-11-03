// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T180 — CLI for the Android native-app UI verbs.

package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/marcelocantos/spyder/internal/clitimeout"
)

// readTextArg resolves --text: "-" reads stdin (preferred for secrets, so
// they stay out of shell history and argv), stripping one trailing newline.
func readTextArg(cmd, v string) string {
	if v != "-" {
		return v
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatalUsage(cmd, fmt.Errorf("read stdin: %v", err))
	}
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s
}

func runInputText(args []string) {
	pf, ctx, cancel := setupCommand("input-text", args,
		[]string{"--text", "--mode", "--as"}, []string{"--secret", "--json"}, clitimeout.DefaultLaunch)
	defer cancel()
	requirePositional("input-text", pf, 1)
	text := readTextArg("input-text", pf.flags["--text"])
	if text == "" {
		fatalUsage("input-text", fmt.Errorf("--text is required (use --text - to read from stdin)"))
	}
	a := map[string]any{
		"device": pf.positional[0],
		"text":   text,
		"owner":  deriveOwner(pf.flags["--as"]),
	}
	if pf.bools["--secret"] {
		a["secret"] = true
	}
	if m := pf.flags["--mode"]; m != "" {
		a["mode"] = m
	}
	dispatchAndExit(ctx, "input_text", a, pf.bools["--json"], !verbose(pf) && !pf.bools["--json"])
}

func runInputKey(args []string) {
	pf, ctx, cancel := setupCommand("input-key", args,
		[]string{"--repeat", "--as"}, []string{"--json"}, clitimeout.DefaultLaunch)
	defer cancel()
	requirePositional("input-key", pf, 2)
	a := map[string]any{
		"device": pf.positional[0],
		"key":    pf.positional[1],
		"owner":  deriveOwner(pf.flags["--as"]),
	}
	if r := pf.flags["--repeat"]; r != "" {
		n, err := strconv.Atoi(r)
		if err != nil || n < 1 {
			fatalUsage("input-key", fmt.Errorf("--repeat: positive integer"))
		}
		a["repeat"] = float64(n)
	}
	dispatchAndExit(ctx, "input_key", a, pf.bools["--json"], !verbose(pf) && !pf.bools["--json"])
}

func runUITree(args []string) {
	pf, ctx, cancel := setupCommand("ui-tree", args,
		[]string{"--as"}, []string{"--compact", "--json"}, clitimeout.DefaultLaunch)
	defer cancel()
	requirePositional("ui-tree", pf, 1)
	dispatchAndExit(ctx, "ui_tree", map[string]any{
		"device":  pf.positional[0],
		"compact": pf.bools["--compact"],
		"owner":   deriveOwner(pf.flags["--as"]),
	}, pf.bools["--json"], false)
}

func runUITap(args []string) {
	pf, ctx, cancel := setupCommand("ui-tap", args,
		[]string{"--text", "--id", "--desc", "--index", "--as"}, []string{"--contains", "--json"}, clitimeout.DefaultLaunch)
	defer cancel()
	requirePositional("ui-tap", pf, 1)
	a := map[string]any{
		"device":      pf.positional[0],
		"text":        pf.flags["--text"],
		"resource_id": pf.flags["--id"],
		"desc":        pf.flags["--desc"],
		"contains":    pf.bools["--contains"],
		"owner":       deriveOwner(pf.flags["--as"]),
	}
	if a["text"] == "" && a["resource_id"] == "" && a["desc"] == "" {
		fatalUsage("ui-tap", fmt.Errorf("one of --text, --id, --desc is required"))
	}
	if v := pf.flags["--index"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fatalUsage("ui-tap", fmt.Errorf("--index: non-negative integer"))
		}
		a["index"] = float64(n)
	}
	dispatchAndExit(ctx, "ui_tap", a, pf.bools["--json"], false)
}

func runScreenSecure(args []string) {
	pf, ctx, cancel := setupCommand("screen-secure", args,
		nil, []string{"--no-screenshot", "--json"}, clitimeout.DefaultLaunch)
	defer cancel()
	requirePositional("screen-secure", pf, 1)
	dispatchAndExit(ctx, "screen_secure", map[string]any{
		"device":     pf.positional[0],
		"screenshot": !pf.bools["--no-screenshot"],
	}, pf.bools["--json"], false)
}
