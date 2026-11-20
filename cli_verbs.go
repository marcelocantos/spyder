// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

// Every daemon verb on the CLI (🎯T198). `spyder <verb>` runs any verb
// in the daemon's verb table — the same operations MCP reaches through
// app_exec — with one flag per schema parameter. Hand-written commands
// (positional sugar such as `deploy <device> <path>`) stay, and accept
// every schema parameter of their verb as a flag too.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marcelocantos/spyder/internal/clitimeout"
	"github.com/marcelocantos/spyder/internal/mcp"
)

// verbTimeoutMargin covers the HTTP round trip on top of the daemon's
// own dispatch deadline.
const verbTimeoutMargin = 30 * time.Second

var verbSchemaCache []mcp.VerbSchema

func verbSchemas() []mcp.VerbSchema {
	if verbSchemaCache == nil {
		verbSchemaCache = mcp.VerbSchemas()
	}
	return verbSchemaCache
}

// lookupVerb finds a verb by name, accepting dashes for underscores.
func lookupVerb(name string) *mcp.VerbSchema {
	name = strings.ReplaceAll(name, "-", "_")
	for i, v := range verbSchemas() {
		if v.Name == name {
			return &verbSchemas()[i]
		}
	}
	return nil
}

func paramOf(v *mcp.VerbSchema, flag string) *mcp.VerbParam {
	name := strings.ReplaceAll(strings.TrimLeft(flag, "-"), "-", "_")
	for i := range v.Params {
		if v.Params[i].Name == name {
			return &v.Params[i]
		}
	}
	return nil
}

// verbFlags are the CLI's own flags on a generic verb command. They
// win over a schema parameter of the same name; `--args` can still
// carry such a parameter.
var verbFlags = map[string]bool{"--timeout": true, "--json": true, "-v": true, "--verbose": true, "--as": true, "--args": true, "--help": true, "-h": true}

// verbInvocation is a parsed generic verb command line.
type verbInvocation struct {
	args    map[string]any
	timeout time.Duration
	json    bool
	verbose bool
	help    bool
}

// parseVerbArgs turns `--param value` flags into the verb's argument
// map, typed by its schema:
//
//	string            --name VALUE
//	number / integer  --count 3
//	boolean           --force, --force=false
//	array             repeat the flag, or pass a JSON array
//	object            repeat --env K=V, or pass a JSON object
//	untyped           JSON if it parses, else the string
//
// Both --name=value and --name value work, and dashes stand for
// underscores (--bundle-id is bundle_id). --args takes the whole
// argument map as JSON (or - for stdin); flags override it.
func parseVerbArgs(v *mcp.VerbSchema, argv []string, stdin io.Reader) (verbInvocation, error) {
	inv := verbInvocation{args: map[string]any{}, timeout: v.Deadline + verbTimeoutMargin}
	flagArgs := map[string]any{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			return inv, fmt.Errorf("unexpected argument %q: %s takes flags only (--name value)", a, v.Name)
		}
		name, value, hasValue := strings.Cut(a, "=")
		next := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(argv) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			i++
			return argv[i], nil
		}
		if verbFlags[name] {
			switch name {
			case "--json":
				inv.json = true
			case "-v", "--verbose":
				inv.verbose = true
			case "--help", "-h":
				inv.help = true
			case "--timeout":
				s, err := next()
				if err != nil {
					return inv, err
				}
				d, err := time.ParseDuration(s)
				if err != nil {
					return inv, fmt.Errorf("--timeout: %v", err)
				}
				inv.timeout = d
			case "--as":
				s, err := next()
				if err != nil {
					return inv, err
				}
				flagArgs["owner"] = s
			case "--args":
				s, err := next()
				if err != nil {
					return inv, err
				}
				if s == "-" {
					b, err := io.ReadAll(stdin)
					if err != nil {
						return inv, fmt.Errorf("--args -: %v", err)
					}
					s = string(b)
				}
				if err := json.Unmarshal([]byte(s), &inv.args); err != nil {
					return inv, fmt.Errorf("--args: want a JSON object: %v", err)
				}
			}
			continue
		}
		p := paramOf(v, name)
		if p == nil {
			return inv, fmt.Errorf("unknown flag %q for %s (see `spyder help %s`)", name, v.Name, v.Name)
		}
		if p.Type == "boolean" {
			b := true
			if hasValue {
				var err error
				if b, err = strconv.ParseBool(value); err != nil {
					return inv, fmt.Errorf("%s: want true or false, got %q", name, value)
				}
			}
			flagArgs[p.Name] = b
			continue
		}
		s, err := next()
		if err != nil {
			return inv, err
		}
		if err := setParam(flagArgs, p, s); err != nil {
			return inv, fmt.Errorf("%s: %v", name, err)
		}
	}
	for k, val := range flagArgs {
		inv.args[k] = val
	}
	if inv.help {
		return inv, nil
	}
	if _, ok := inv.args["owner"]; !ok && paramOf(v, "owner") != nil {
		inv.args["owner"] = deriveOwner("")
	}
	var missing []string
	for _, p := range v.Params {
		if _, ok := inv.args[p.Name]; p.Required && !ok {
			missing = append(missing, "--"+strings.ReplaceAll(p.Name, "_", "-"))
		}
	}
	if len(missing) > 0 {
		return inv, fmt.Errorf("%s requires %s", v.Name, strings.Join(missing, ", "))
	}
	return inv, nil
}

// setParam stores one flag value under its schema type.
func setParam(args map[string]any, p *mcp.VerbParam, s string) error {
	switch p.Type {
	case "string":
		args[p.Name] = s
	case "number", "integer":
		n, err := parseNumber(s)
		if err != nil {
			return err
		}
		args[p.Name] = n
	case "array":
		if strings.HasPrefix(strings.TrimSpace(s), "[") {
			var list []any
			if err := json.Unmarshal([]byte(s), &list); err != nil {
				return fmt.Errorf("want a JSON array: %v", err)
			}
			args[p.Name] = list
			return nil
		}
		var elem any = s
		if p.ItemType == "number" || p.ItemType == "integer" {
			n, err := parseNumber(s)
			if err != nil {
				return err
			}
			elem = n
		}
		list, _ := args[p.Name].([]any)
		args[p.Name] = append(list, elem)
	case "object":
		if strings.HasPrefix(strings.TrimSpace(s), "{") {
			var obj map[string]any
			if err := json.Unmarshal([]byte(s), &obj); err != nil {
				return fmt.Errorf("want a JSON object: %v", err)
			}
			args[p.Name] = obj
			return nil
		}
		k, val, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("want K=V or a JSON object, got %q", s)
		}
		obj, _ := args[p.Name].(map[string]any)
		if obj == nil {
			obj = map[string]any{}
		}
		obj[k] = val
		args[p.Name] = obj
	default:
		var anyVal any
		if err := json.Unmarshal([]byte(s), &anyVal); err == nil {
			args[p.Name] = anyVal
		} else {
			args[p.Name] = s
		}
	}
	return nil
}

func parseNumber(s string) (float64, error) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("want a number, got %q", s)
	}
	return n, nil
}

// runVerb is `spyder <verb> [--param value ...]`.
func runVerb(v *mcp.VerbSchema, argv []string) {
	inv, err := parseVerbArgs(v, argv, os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spyder %s: %v\n\n%s", v.Name, err, verbUsage(v))
		os.Exit(2)
	}
	if inv.help {
		fmt.Print(verbUsage(v))
		return
	}
	ctx, cancel := clitimeout.Context(inv.timeout)
	defer cancel()
	dispatchAndExit(ctx, v.Name, inv.args, inv.json, false)
}

// verbUsage is a verb's help, from the schema MCP uses.
func verbUsage(v *mcp.VerbSchema) string {
	var b strings.Builder
	fmt.Fprintf(&b, "spyder %s", v.Name)
	for _, p := range v.Params {
		if p.Required {
			fmt.Fprintf(&b, " --%s %s", strings.ReplaceAll(p.Name, "_", "-"), strings.ToUpper(p.Name))
		}
	}
	b.WriteString(" [flags]\n\n")
	b.WriteString(v.Description)
	b.WriteString("\n\nparameters:\n")
	for _, p := range v.Params {
		typ := p.Type
		if typ == "array" && p.ItemType != "" {
			typ = p.ItemType + "[] (repeat the flag)"
		}
		if typ == "object" {
			typ = "object (JSON or repeated K=V)"
		}
		if typ == "" {
			typ = "any (JSON)"
		}
		req := ""
		if p.Required {
			req = ", required"
		}
		fmt.Fprintf(&b, "  --%s  %s%s\n", strings.ReplaceAll(p.Name, "_", "-"), typ, req)
		if p.Description != "" {
			fmt.Fprintf(&b, "      %s\n", strings.ReplaceAll(p.Description, "\n", "\n      "))
		}
	}
	b.WriteString("\ncommon flags: --as OWNER, --args JSON|-, --json, --timeout DURATION, -v\n")
	return b.String()
}

// verbList is the verb section of `spyder help`.
func verbList() string {
	var names []string
	for _, v := range verbSchemas() {
		names = append(names, v.Name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("\nEvery daemon verb also runs as `spyder <verb> --param value ...` (the same\noperations and parameters MCP reaches through app_exec). `spyder help <verb>`\nlists a verb's parameters. Verbs:\n")
	line := " "
	for _, n := range names {
		if len(line)+len(n)+1 > 78 {
			b.WriteString(line + "\n")
			line = " "
		}
		line += " " + n
	}
	b.WriteString(line + "\n")
	return b.String()
}

// runHelp is `spyder help [command|verb]`.
func runHelp(args []string) {
	if len(args) == 0 {
		fmt.Print(usage)
		fmt.Print(verbList())
		return
	}
	name := args[0]
	if c := lookupCLI(name); c != nil {
		fmt.Println(c.usage)
		if verb, ok := cliVerbOf[name]; ok {
			fmt.Printf("\nThis command calls the %s verb, and also takes its parameters as flags:\n\n", verb)
			fmt.Print(verbUsage(lookupVerb(verb)))
		}
		return
	}
	if v := lookupVerb(name); v != nil {
		fmt.Print(verbUsage(v))
		return
	}
	fmt.Fprintf(os.Stderr, "unknown command or verb %q\n", name)
	os.Exit(1)
}

// cliVerbOf maps each hand-written command that calls exactly one verb
// to that verb. Such a command takes every parameter of its verb as a
// flag, beyond its own flags and positionals (passthrough below).
// TestCLIVerbMapMatchesSource checks this table against the code.
var cliVerbOf = map[string]string{
	"app-perf-get":    "app_perf_get",
	"baseline":        "baseline_update",
	"battery-history": "battery_history",
	"crashes":         "crashes",
	"deploy":          "deploy_app",
	"dev-policy":      "dev_policy",
	"device-setting":  "device_setting",
	"device-state":    "device_state",
	"devices":         "devices",
	"diff":            "diff",
	"exec":            "app_exec",
	"input-key":       "input_key",
	"input-swipe":     "input_swipe",
	"input-tap":       "input_tap",
	"input-text":      "input_text",
	"install":         "install_app",
	"is-running":      "is_running",
	"launch-app":      "launch_app",
	"launch-player":   "launch_player",
	"list-apps":       "list_apps",
	"list-scripts":    "list_scripts",
	"net":             "network",
	"perf-fps":        "perf_fps",
	"release":         "release",
	"renew":           "renew",
	"reservations":    "reservations",
	"reserve":         "reserve",
	"resolve":         "resolve",
	"rotate":          "rotate",
	"run-script":      "run_script",
	"screen-secure":   "screen_secure",
	"screenshot":      "screenshot",
	"terminate-app":   "terminate_app",
	"ui-tap":          "ui_tap",
	"ui-tree":         "ui_tree",
	"uninstall":       "uninstall_app",
	"verify":          "verify",
	"verify-answer":   "verify_answer",
	"verify-dismiss":  "verify_dismiss",
	"verify-report":   "verify_report",
	"verify-status":   "verify_status",
	"wait-state":      "wait_state",
}

// passthrough holds the verb parameters a hand-written command was
// given as flags it does not itself know. setupCommand fills it and
// postTool merges it into that verb's call. A CLI process runs one
// command, so one slot is enough.
var passthrough struct {
	verb string
	args map[string]any
}

// extractPassthrough removes from argv the flags that are parameters
// of the command's verb but not the command's own flags, and records
// them for postTool. Values the command sets itself win.
func extractPassthrough(command string, argv []string, own map[string]bool) ([]string, error) {
	verbName, ok := cliVerbOf[command]
	if !ok {
		return argv, nil
	}
	v := lookupVerb(verbName)
	if v == nil {
		return argv, nil
	}
	var kept []string
	args := map[string]any{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		name, value, hasValue := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "--") || own[name] || verbFlags[name] {
			kept = append(kept, a)
			continue
		}
		p := paramOf(v, name)
		if p == nil {
			kept = append(kept, a)
			continue
		}
		if p.Type == "boolean" {
			b := true
			if hasValue {
				var err error
				if b, err = strconv.ParseBool(value); err != nil {
					return nil, fmt.Errorf("%s: want true or false, got %q", name, value)
				}
			}
			args[p.Name] = b
			continue
		}
		if !hasValue {
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("%s requires a value", name)
			}
			i++
			value = argv[i]
		}
		if err := setParam(args, p, value); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
	}
	if len(args) > 0 {
		passthrough.verb, passthrough.args = verbName, args
	}
	return kept, nil
}

// mergePassthrough adds recorded passthrough parameters to a call of
// their verb, without overriding what the command set.
func mergePassthrough(tool string, args map[string]any) map[string]any {
	if passthrough.verb != tool || len(passthrough.args) == 0 {
		return args
	}
	if args == nil {
		args = map[string]any{}
	}
	for k, v := range passthrough.args {
		if _, set := args[k]; !set {
			args[k] = v
		}
	}
	return args
}

// runExec is `spyder exec`: an app_exec script — MCP's single tool —
// from a file, stdin, or --script. The file is read here, on the
// caller's side, so relative paths mean what the caller means.
func runExec(args []string) {
	pf, ctx, cancel := setupCommand("exec", args, []string{"--script", "--max-duration-ms"}, []string{"--json"}, 0)
	defer cancel()
	a := map[string]any{}
	script := pf.flags["--script"]
	switch {
	case script != "" && len(pf.positional) > 0:
		fatalUsage("exec", fmt.Errorf("give a script file or --script, not both"))
	case script == "" && len(pf.positional) != 1:
		fatalUsage("exec", fmt.Errorf("want one script file (or - for stdin), or --script TEXT"))
	case script == "":
		var b []byte
		var err error
		if pf.positional[0] == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else {
			b, err = os.ReadFile(pf.positional[0])
		}
		if err != nil {
			fatalUsage("exec", err)
		}
		script = string(b)
	}
	a["script"] = script
	budget := lookupVerb("app_exec").Deadline
	if ms := pf.flags["--max-duration-ms"]; ms != "" {
		n, err := strconv.Atoi(ms)
		if err != nil || n <= 0 {
			fatalUsage("exec", fmt.Errorf("--max-duration-ms: positive integer"))
		}
		a["max_duration_ms"] = n
		if d := time.Duration(n) * time.Millisecond; d > budget {
			budget = d
		}
	}
	if pf.flags["--timeout"] == "" {
		cancel()
		ctx, cancel = clitimeout.Context(budget + verbTimeoutMargin)
		defer cancel()
	}
	dispatchAndExit(ctx, "app_exec", a, pf.bools["--json"], false)
}
