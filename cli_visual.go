// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/marcelocantos/spyder/internal/cliexit"
	"github.com/marcelocantos/spyder/internal/clitimeout"
)

// runDiff implements `spyder diff <suite>/<case> <screenshot> [<manifest>]
// [--variant V] [--tolerance F] [--json]`.
//
// It compares the screenshot against the stored baseline and prints the
// structured diff report. The Pass verdict is surfaced as the exit code:
//
//	0 — pass (within tolerance)
//	1 — fail (structural or pixel change outside tolerance)
//	2 — argument error
func runDiff(args []string) {
	pf, ctx, cancel := setupCommand("diff", args,
		[]string{"--variant", "--tolerance"},
		[]string{"--json"},
		clitimeout.DefaultRead,
	)
	defer cancel()
	if len(pf.positional) < 2 || len(pf.positional) > 3 {
		fatalUsage("diff", fmt.Errorf("expected <suite>/<case> <screenshot> [<manifest>]"))
	}
	suiteCase := pf.positional[0]
	suite, caseName, ok := splitSuiteCase(suiteCase)
	if !ok {
		fatalUsage("diff", fmt.Errorf("<suite>/<case> must contain exactly one '/'"))
	}
	screenshotPath := pf.positional[1]

	a := map[string]any{
		"suite":           suite,
		"case":            caseName,
		"screenshot_path": screenshotPath,
	}
	if v := pf.flags["--variant"]; v != "" {
		a["variant"] = v
	}
	if t := pf.flags["--tolerance"]; t != "" {
		// Parse as float; pass as string and let the server validate.
		// The server accepts number type; we pass it via JSON float.
		var f float64
		if _, serr := fmt.Sscanf(t, "%f", &f); serr != nil {
			fatalUsage("diff", fmt.Errorf("--tolerance: not a number: %q", t))
		}
		a["pixel_tolerance"] = f
	}
	if len(pf.positional) == 3 {
		manifestRaw, rerr := os.ReadFile(pf.positional[2])
		if rerr != nil {
			cliexit.Errorf(cliexit.ExitGeneric, "spyder diff: read manifest %q: %v", pf.positional[2], rerr)
		}
		a["manifest"] = string(manifestRaw)
	}

	res, err := postTool(ctx, "diff", a)
	if err != nil {
		cliexit.Errorf(daemonExitCode(err), "spyder diff: %v", err)
	}
	if res.IsError {
		text := res.firstText()
		cliexit.Errorf(cliexit.MapDaemonError(0, "", text), "%s", text)
	}
	renderResult(res, pf.bools["--json"], false)

	// Parse the Pass field from the JSON and exit non-zero on fail.
	if !pf.bools["--json"] {
		// Already printed; just exit 0.
		return
	}
	// In --json mode, parse the report and exit 1 if Pass=false.
	text := res.firstText()
	if strings.Contains(text, `"pass": false`) || strings.Contains(text, `"pass":false`) {
		cliexit.Exit(cliexit.ExitGeneric)
	}
}

// runBaseline implements `spyder baseline <update> <suite>/<case> <screenshot>
// [<manifest>] [--variant V]`.
//
// Currently only the `update` subcommand is defined.
func runBaseline(args []string) {
	if len(args) == 0 {
		fatalUsage("baseline", fmt.Errorf("missing subcommand — expected: update"))
	}
	switch args[0] {
	case "update":
		runBaselineUpdate(args[1:])
	default:
		fatalUsage("baseline", fmt.Errorf("unknown subcommand %q — expected: update", args[0]))
	}
}

// runBaselineUpdate implements `spyder baseline update <suite>/<case>
// <screenshot> [<manifest>] [--variant V]`.
func runBaselineUpdate(args []string) {
	pf, ctx, cancel := setupCommand("baseline", args, []string{"--variant"}, nil, clitimeout.DefaultRead)
	defer cancel()
	if len(pf.positional) < 2 || len(pf.positional) > 3 {
		fatalUsage("baseline", fmt.Errorf("expected <suite>/<case> <screenshot> [<manifest>]"))
	}
	suiteCase := pf.positional[0]
	suite, caseName, ok := splitSuiteCase(suiteCase)
	if !ok {
		fatalUsage("baseline", fmt.Errorf("<suite>/<case> must contain exactly one '/'"))
	}

	a := map[string]any{
		"suite":           suite,
		"case":            caseName,
		"screenshot_path": pf.positional[1],
	}
	if v := pf.flags["--variant"]; v != "" {
		a["variant"] = v
	}
	if len(pf.positional) == 3 {
		manifestRaw, rerr := os.ReadFile(pf.positional[2])
		if rerr != nil {
			cliexit.Errorf(cliexit.ExitGeneric, "spyder baseline update: read manifest %q: %v",
				pf.positional[2], rerr)
		}
		a["manifest"] = string(manifestRaw)
	}
	dispatchAndExit(ctx, "baseline_update", a, false, !verbose(pf))
}

// splitSuiteCase splits "suite/case" into ("suite", "case", true).
// Returns ("", "", false) if there is no '/' or more than one '/'.
func splitSuiteCase(s string) (suite, caseName string, ok bool) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
