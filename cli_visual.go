// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"
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
	pf, err := parseFlags(args,
		[]string{"--variant", "--tolerance"},
		[]string{"--json"},
	)
	if err != nil {
		fatalUsage("diff", err)
	}
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
			fmt.Fprintf(os.Stderr, "spyder diff: read manifest %q: %v\n", pf.positional[2], rerr)
			os.Exit(1)
		}
		a["manifest"] = string(manifestRaw)
	}

	res, err := postTool("diff", a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spyder diff: %v\n", err)
		os.Exit(1)
	}
	if res.IsError {
		fmt.Fprintln(os.Stderr, res.firstText())
		os.Exit(1)
	}
	renderResult(res, pf.bools["--json"])

	// Parse the Pass field from the JSON and exit non-zero on fail.
	if !pf.bools["--json"] {
		// Already printed; just exit 0.
		return
	}
	// In --json mode, parse the report and exit 1 if Pass=false.
	text := res.firstText()
	if strings.Contains(text, `"pass": false`) || strings.Contains(text, `"pass":false`) {
		os.Exit(1)
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
	pf, err := parseFlags(args, []string{"--variant"}, nil)
	if err != nil {
		fatalUsage("baseline", err)
	}
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
			fmt.Fprintf(os.Stderr, "spyder baseline update: read manifest %q: %v\n",
				pf.positional[2], rerr)
			os.Exit(1)
		}
		a["manifest"] = string(manifestRaw)
	}
	dispatchAndExit("baseline_update", a, false)
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
