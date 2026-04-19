// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/rest"
)

// daemonURLEnv overrides the REST base URL for `spyder <tool>`
// subcommands. Default is http://127.0.0.1:3030.
const daemonURLEnv = "SPYDER_DAEMON_URL"

// defaultDaemonURL points at the CLI subcommands' REST target.
const defaultDaemonURL = "http://" + defaultAddr

// cliCommand is a single CLI subcommand definition. run receives the
// post-subcommand arguments (everything after `spyder <name>`).
type cliCommand struct {
	name  string
	usage string
	run   func(args []string)
}

// cliCommands lists all the device-tool CLI subcommands that proxy to
// the local daemon's REST surface. Populated in init() to break the
// init cycle caused by run*() helpers calling fatalUsage → lookupCLI.
var cliCommands []cliCommand

func init() {
	cliCommands = []cliCommand{
		{"devices", "spyder devices [--platform ios|android|all] [--json]", runDevices},
		{"resolve", "spyder resolve <name> [--json]", runResolve},
		{"device-state", "spyder device-state <device> [--json]", runDeviceState},
		{"screenshot", "spyder screenshot <device> [--output FILE] [--as OWNER]", runScreenshot},
		{"keepawake", "spyder keepawake <device> [--as OWNER]", runKeepAwake},
		{"list-apps", "spyder list-apps <device> [--json]", runListApps},
		{"launch-app", "spyder launch-app <device> <bundle-id> [--as OWNER]", runLaunchApp},
		{"terminate-app", "spyder terminate-app <device> <bundle-id> [--as OWNER]", runTerminateApp},
		{"install", "spyder install <device> <path> [--as OWNER]", runInstall},
		{"uninstall", "spyder uninstall <device> <bundle-id> [--as OWNER]", runUninstall},
		{"deploy", "spyder deploy <device> <path> [--bundle-id ID] [--as OWNER]", runDeploy},
		{"reserve", "spyder reserve <device> [--as OWNER] [--ttl SECONDS] [--note TEXT]", runReserve},
		{"release", "spyder release <device> [--as OWNER]", runRelease},
		{"renew", "spyder renew <device> [--as OWNER] [--ttl SECONDS]", runRenew},
		{"reservations", "spyder reservations [--json]", runReservations},
		{"runs", "spyder runs <list|show|artefacts> [args...]", runRuns},
		{"rotate", "spyder rotate <device> --to <orientation> [--as OWNER]", runRotate},
		{"crashes", "spyder crashes <device> [--since RFC3339] [--process NAME] [--as OWNER] [--json]", runCrashes},
		{"sim", "spyder sim <list|create|boot|shutdown|delete> [args...]", runSim},
		{"emu", "spyder emu <list|create|boot|shutdown|delete> [args...]", runEmu},
		{"record", "spyder record <device> --start | --stop [--as OWNER]", runRecord},
	}
}

// lookupCLI returns the cliCommand for name, or nil.
func lookupCLI(name string) *cliCommand {
	for i := range cliCommands {
		if cliCommands[i].name == name {
			return &cliCommands[i]
		}
	}
	return nil
}

// daemonBaseURL returns the REST base URL (scheme://host:port) from
// env or default.
func daemonBaseURL() string {
	if v := os.Getenv(daemonURLEnv); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultDaemonURL
}

// toolResultContent is the portion of mcp.CallToolResult we consume:
// a stream of text/image blocks plus an isError flag.
type toolResultContent struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Data     string `json:"data,omitempty"`
		MIMEType string `json:"mimeType,omitempty"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// postTool POSTs args to /api/v1/<tool> on the local daemon and
// returns the parsed result or a transport-level error. Tool errors
// (result.isError=true) are returned as-is in the result; callers
// decide how to surface them. If the first call fails with
// ECONNREFUSED *and* the CLI is targeting the default loopback daemon,
// it tries to spawn a detached `spyder serve` and retry once. Users
// who set SPYDER_DAEMON_URL to a remote daemon get a plain error.
func postTool(tool string, args map[string]any) (*toolResultContent, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode args: %w", err)
	}
	base := daemonBaseURL()
	url := base + rest.Prefix + tool
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil && isConnRefused(err) && base == defaultDaemonURL {
		if spawnErr := autoStartDaemon(); spawnErr != nil {
			return nil, fmt.Errorf("daemon not reachable at %s and auto-start failed: %v — try `brew services start spyder`",
				base, spawnErr)
		}
		resp, err = http.Post(url, "application/json", bytes.NewReader(body))
	}
	if err != nil {
		if isConnRefused(err) {
			return nil, fmt.Errorf("daemon not reachable at %s — start it with `brew services start spyder` or `spyder serve`",
				base)
		}
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("daemon %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out toolResultContent
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse daemon response: %w (body: %s)", err, raw)
	}
	return &out, nil
}

// autoStartDaemon spawns a detached `spyder serve` process, logs its
// output to ~/.spyder/daemon.log, and polls the listener until it's
// reachable or a short timeout expires. Safe to call when the daemon
// is already up — the new process will fail to bind and exit, and
// the probe will succeed on the running instance.
func autoStartDaemon() error {
	logPath := filepath.Join(paths.Base(), "daemon.log")
	if err := os.MkdirAll(paths.Base(), 0o755); err != nil {
		return fmt.Errorf("prepare %s: %w", paths.Base(), err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}

	self, err := os.Executable()
	if err != nil {
		logFile.Close()
		return fmt.Errorf("resolve self path: %w", err)
	}
	cmd := exec.Command(self, "serve")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("spawn: %w", err)
	}
	// Release the file handle in the parent; the child keeps its copy.
	logFile.Close()
	// Detach so we don't zombie the child after exit.
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		// A zero-length POST to a nonexistent tool gets either 404
		// (daemon up) or ECONNREFUSED (still coming up). Anything
		// other than ECONNREFUSED means the listener is live.
		resp, probeErr := http.Post(defaultDaemonURL+rest.Prefix+"__probe__",
			"application/json", nil)
		if probeErr == nil {
			resp.Body.Close()
			return nil
		}
		if !isConnRefused(probeErr) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready within 4s (check %s)", logPath)
}

// isConnRefused returns true when err is an ECONNREFUSED, regardless
// of the wrapper depth net/http buried it under.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

// firstText returns the first text content block's text, or "".
func (r *toolResultContent) firstText() string {
	for _, c := range r.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// firstImage returns the decoded bytes + MIME type of the first image
// content block, or (nil, "", false) when none is present.
func (r *toolResultContent) firstImage() ([]byte, string, bool) {
	for _, c := range r.Content {
		if c.Type == "image" {
			b, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil {
				return nil, "", false
			}
			return b, c.MIMEType, true
		}
	}
	return nil, "", false
}

// renderResult prints result text to stdout and exits non-zero on
// tool error. `json` mode prints the first text block verbatim
// (handlers that return structured data already produce JSON).
func renderResult(r *toolResultContent, jsonMode bool) {
	text := r.firstText()
	if r.IsError {
		fmt.Fprintln(os.Stderr, text)
		os.Exit(1)
	}
	if jsonMode {
		fmt.Println(text)
		return
	}
	// In non-JSON mode, try pretty-print. For JSON payloads (devices,
	// resolve, reservations) we keep the indented text as-is — it's
	// already readable. For plain confirmations we print unchanged.
	fmt.Println(text)
}

// parseFlags is a tiny argv parser for the CLI subcommands. Returns
// positional args + a map of string flags + a map of bool flags. It
// stops at the first non-flag token; all subsequent tokens are
// positional.
type parsedFlags struct {
	positional []string
	flags      map[string]string
	bools      map[string]bool
}

func parseFlags(args []string, stringFlags, boolFlags []string) (parsedFlags, error) {
	isString := map[string]bool{}
	for _, f := range stringFlags {
		isString[f] = true
	}
	isBool := map[string]bool{}
	for _, f := range boolFlags {
		isBool[f] = true
	}

	out := parsedFlags{flags: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			out.positional = append(out.positional, a)
			continue
		}
		if isBool[a] {
			out.bools[a] = true
			continue
		}
		if isString[a] {
			if i+1 >= len(args) {
				return parsedFlags{}, fmt.Errorf("%s requires a value", a)
			}
			out.flags[a] = args[i+1]
			i++
			continue
		}
		return parsedFlags{}, fmt.Errorf("unknown flag %q", a)
	}
	return out, nil
}

// requirePositional extracts exactly n positional args or exits with a
// usage error.
func requirePositional(name string, pf parsedFlags, n int) {
	if len(pf.positional) != n {
		cmd := lookupCLI(name)
		fmt.Fprintf(os.Stderr, "%s: expected %d positional arg(s), got %d\n", name, n, len(pf.positional))
		if cmd != nil {
			fmt.Fprintf(os.Stderr, "%s\n", cmd.usage)
		}
		os.Exit(2)
	}
}

// dispatchAndExit runs postTool, prints the result, and exits.
func dispatchAndExit(tool string, args map[string]any, jsonMode bool) {
	res, err := postTool(tool, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spyder %s: %v\n", tool, err)
		os.Exit(1)
	}
	renderResult(res, jsonMode)
}

// --- subcommand implementations -------------------------------------

func runDevices(args []string) {
	pf, err := parseFlags(args, []string{"--platform"}, []string{"--json"})
	if err != nil {
		fatalUsage("devices", err)
	}
	a := map[string]any{}
	if p := pf.flags["--platform"]; p != "" {
		a["platform"] = p
	}
	dispatchAndExit("devices", a, pf.bools["--json"])
}

func runResolve(args []string) {
	pf, err := parseFlags(args, nil, []string{"--json"})
	if err != nil {
		fatalUsage("resolve", err)
	}
	requirePositional("resolve", pf, 1)
	dispatchAndExit("resolve",
		map[string]any{"name": pf.positional[0]},
		pf.bools["--json"])
}

func runDeviceState(args []string) {
	pf, err := parseFlags(args, nil, []string{"--json"})
	if err != nil {
		fatalUsage("device-state", err)
	}
	requirePositional("device-state", pf, 1)
	dispatchAndExit("device_state",
		map[string]any{"device": pf.positional[0]},
		pf.bools["--json"])
}

func runScreenshot(args []string) {
	pf, err := parseFlags(args, []string{"--output", "--as"}, nil)
	if err != nil {
		fatalUsage("screenshot", err)
	}
	requirePositional("screenshot", pf, 1)
	dev := pf.positional[0]
	a := map[string]any{"device": dev}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}

	res, err := postTool("screenshot", a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spyder screenshot: %v\n", err)
		os.Exit(1)
	}
	if res.IsError {
		fmt.Fprintln(os.Stderr, res.firstText())
		os.Exit(1)
	}
	png, mime, ok := res.firstImage()
	if !ok {
		fmt.Fprintf(os.Stderr, "spyder screenshot: no image in response\n")
		os.Exit(1)
	}
	output := pf.flags["--output"]
	if output == "" {
		output = fmt.Sprintf("%s-%s.png",
			sanitizeFilenameComponent(dev),
			time.Now().Format("20060102-150405"))
	}
	if err := os.WriteFile(output, png, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "spyder screenshot: writing %s: %v\n", output, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes, %s)\n", output, len(png), mime)
}

func runKeepAwake(args []string) {
	pf, err := parseFlags(args, []string{"--as"}, nil)
	if err != nil {
		fatalUsage("keepawake", err)
	}
	requirePositional("keepawake", pf, 1)
	a := map[string]any{"device": pf.positional[0]}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}
	dispatchAndExit("keepawake", a, false)
}

func runListApps(args []string) {
	pf, err := parseFlags(args, nil, []string{"--json"})
	if err != nil {
		fatalUsage("list-apps", err)
	}
	requirePositional("list-apps", pf, 1)
	dispatchAndExit("list_apps",
		map[string]any{"device": pf.positional[0]},
		pf.bools["--json"])
}

func runLaunchApp(args []string) {
	pf, err := parseFlags(args, []string{"--as"}, nil)
	if err != nil {
		fatalUsage("launch-app", err)
	}
	requirePositional("launch-app", pf, 2)
	a := map[string]any{
		"device":    pf.positional[0],
		"bundle_id": pf.positional[1],
	}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}
	dispatchAndExit("launch_app", a, false)
}

func runTerminateApp(args []string) {
	pf, err := parseFlags(args, []string{"--as"}, nil)
	if err != nil {
		fatalUsage("terminate-app", err)
	}
	requirePositional("terminate-app", pf, 2)
	a := map[string]any{
		"device":    pf.positional[0],
		"bundle_id": pf.positional[1],
	}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}
	dispatchAndExit("terminate_app", a, false)
}

func runInstall(args []string) {
	pf, err := parseFlags(args, []string{"--as"}, nil)
	if err != nil {
		fatalUsage("install", err)
	}
	requirePositional("install", pf, 2)
	a := map[string]any{
		"device": pf.positional[0],
		"path":   pf.positional[1],
	}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}
	dispatchAndExit("install_app", a, false)
}

func runUninstall(args []string) {
	pf, err := parseFlags(args, []string{"--as"}, nil)
	if err != nil {
		fatalUsage("uninstall", err)
	}
	requirePositional("uninstall", pf, 2)
	a := map[string]any{
		"device":    pf.positional[0],
		"bundle_id": pf.positional[1],
	}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}
	dispatchAndExit("uninstall_app", a, false)
}

func runDeploy(args []string) {
	pf, err := parseFlags(args, []string{"--as", "--bundle-id"}, nil)
	if err != nil {
		fatalUsage("deploy", err)
	}
	requirePositional("deploy", pf, 2)
	a := map[string]any{
		"device": pf.positional[0],
		"path":   pf.positional[1],
	}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}
	if bid := pf.flags["--bundle-id"]; bid != "" {
		a["bundle_id"] = bid
	}
	dispatchAndExit("deploy_app", a, false)
}

func runReserve(args []string) {
	pf, err := parseFlags(args, []string{"--as", "--ttl", "--note"}, nil)
	if err != nil {
		fatalUsage("reserve", err)
	}
	requirePositional("reserve", pf, 1)
	a := map[string]any{"device": pf.positional[0]}
	a["owner"] = deriveOwner(pf.flags["--as"])
	if v := pf.flags["--ttl"]; v != "" {
		n, perr := parsePositiveInt(v)
		if perr != nil {
			fatalUsage("reserve", fmt.Errorf("--ttl: %v", perr))
		}
		a["ttl_seconds"] = n
	}
	if v := pf.flags["--note"]; v != "" {
		a["note"] = v
	}
	dispatchAndExit("reserve", a, false)
}

func runRelease(args []string) {
	pf, err := parseFlags(args, []string{"--as"}, nil)
	if err != nil {
		fatalUsage("release", err)
	}
	requirePositional("release", pf, 1)
	a := map[string]any{
		"device": pf.positional[0],
		"owner":  deriveOwner(pf.flags["--as"]),
	}
	dispatchAndExit("release", a, false)
}

func runRenew(args []string) {
	pf, err := parseFlags(args, []string{"--as", "--ttl"}, nil)
	if err != nil {
		fatalUsage("renew", err)
	}
	requirePositional("renew", pf, 1)
	a := map[string]any{
		"device": pf.positional[0],
		"owner":  deriveOwner(pf.flags["--as"]),
	}
	if v := pf.flags["--ttl"]; v != "" {
		n, perr := parsePositiveInt(v)
		if perr != nil {
			fatalUsage("renew", fmt.Errorf("--ttl: %v", perr))
		}
		a["ttl_seconds"] = n
	}
	dispatchAndExit("renew", a, false)
}

func runReservations(args []string) {
	pf, err := parseFlags(args, nil, []string{"--json"})
	if err != nil {
		fatalUsage("reservations", err)
	}
	dispatchAndExit("reservations", map[string]any{}, pf.bools["--json"])
}

// runRuns dispatches `spyder runs <subcommand>` — a two-level
// subcommand group for run-artefact inspection. Kept close to the
// flat-subcommand style above; each leaf is a tiny REST wrapper.
func runRuns(args []string) {
	if len(args) == 0 {
		fatalUsage("runs", fmt.Errorf("missing subcommand — expected list|show|artefacts"))
	}
	switch args[0] {
	case "list":
		runRunsList(args[1:])
	case "show":
		runRunsShow(args[1:])
	case "artefacts":
		runRunsArtefacts(args[1:])
	default:
		fatalUsage("runs", fmt.Errorf("unknown subcommand %q — expected list|show|artefacts", args[0]))
	}
}

func runRunsList(args []string) {
	pf, err := parseFlags(args, nil, []string{"--json"})
	if err != nil {
		fatalUsage("runs", err)
	}
	dispatchAndExit("runs_list", map[string]any{}, pf.bools["--json"])
}

func runRunsShow(args []string) {
	pf, err := parseFlags(args, nil, []string{"--json"})
	if err != nil {
		fatalUsage("runs", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("runs", fmt.Errorf("show expects one run-id"))
	}
	dispatchAndExit("runs_show",
		map[string]any{"run_id": pf.positional[0]},
		pf.bools["--json"])
}

// runRunsArtefacts reuses runs_show and extracts just the artefacts
// array so scripts can pipe it. Defaults to a tabular render; --json
// emits the raw array.
func runRunsArtefacts(args []string) {
	pf, err := parseFlags(args, nil, []string{"--json"})
	if err != nil {
		fatalUsage("runs", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("runs", fmt.Errorf("artefacts expects one run-id"))
	}
	res, err := postTool("runs_show", map[string]any{"run_id": pf.positional[0]})
	if err != nil {
		fmt.Fprintf(os.Stderr, "spyder runs artefacts: %v\n", err)
		os.Exit(1)
	}
	if res.IsError {
		fmt.Fprintln(os.Stderr, res.firstText())
		os.Exit(1)
	}
	var run struct {
		ID        string `json:"id"`
		Artefacts []struct {
			Name      string `json:"name"`
			Source    string `json:"source"`
			MIMEType  string `json:"mime_type"`
			Size      int64  `json:"size"`
			CreatedAt string `json:"created_at"`
		} `json:"artefacts"`
	}
	if err := json.Unmarshal([]byte(res.firstText()), &run); err != nil {
		fmt.Fprintf(os.Stderr, "spyder runs artefacts: parse: %v\n", err)
		os.Exit(1)
	}
	if pf.bools["--json"] {
		data, _ := json.MarshalIndent(run.Artefacts, "", "  ")
		fmt.Println(string(data))
		return
	}
	if len(run.Artefacts) == 0 {
		fmt.Printf("no artefacts recorded for %s\n", run.ID)
		return
	}
	fmt.Printf("%-40s %-12s %-20s %10s %s\n", "NAME", "SOURCE", "MIME", "SIZE", "CREATED")
	for _, a := range run.Artefacts {
		fmt.Printf("%-40s %-12s %-20s %10d %s\n",
			a.Name, a.Source, a.MIMEType, a.Size, a.CreatedAt)
	}
}

func runRotate(args []string) {
	pf, err := parseFlags(args, []string{"--to", "--as"}, nil)
	if err != nil {
		fatalUsage("rotate", err)
	}
	requirePositional("rotate", pf, 1)
	orientation := pf.flags["--to"]
	if orientation == "" {
		fatalUsage("rotate", fmt.Errorf("--to is required (portrait, landscape-left, landscape-right, portrait-upside-down)"))
	}
	a := map[string]any{
		"device":      pf.positional[0],
		"orientation": orientation,
	}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}
	dispatchAndExit("rotate", a, false)
}

func runCrashes(args []string) {
	pf, err := parseFlags(args, []string{"--since", "--process", "--as"}, []string{"--json"})
	if err != nil {
		fatalUsage("crashes", err)
	}
	requirePositional("crashes", pf, 1)
	a := map[string]any{"device": pf.positional[0]}
	if s := pf.flags["--since"]; s != "" {
		a["since"] = s
	}
	if p := pf.flags["--process"]; p != "" {
		a["process"] = p
	}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	}
	dispatchAndExit("crashes", a, pf.bools["--json"])
}

// --- sim subcommands ------------------------------------------------

// runSim dispatches `spyder sim <subcommand>` for iOS simulator lifecycle.
func runSim(args []string) {
	if len(args) == 0 {
		fatalUsage("sim", fmt.Errorf("missing subcommand — expected list|create|boot|shutdown|delete"))
	}
	switch args[0] {
	case "list":
		runSimList(args[1:])
	case "create":
		runSimCreate(args[1:])
	case "boot":
		runSimBoot(args[1:])
	case "shutdown":
		runSimShutdown(args[1:])
	case "delete":
		runSimDelete(args[1:])
	default:
		fatalUsage("sim", fmt.Errorf("unknown subcommand %q — expected list|create|boot|shutdown|delete", args[0]))
	}
}

func runSimList(args []string) {
	pf, err := parseFlags(args, []string{"--state"}, []string{"--json"})
	if err != nil {
		fatalUsage("sim", err)
	}
	a := map[string]any{}
	if s := pf.flags["--state"]; s != "" {
		a["state"] = s
	}
	dispatchAndExit("sim_list", a, pf.bools["--json"])
}

func runSimCreate(args []string) {
	pf, err := parseFlags(args, []string{"--type", "--runtime"}, []string{"--json"})
	if err != nil {
		fatalUsage("sim", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("sim", fmt.Errorf("create: expected <name> (with --type and --runtime flags)"))
	}
	deviceType := pf.flags["--type"]
	if deviceType == "" {
		fatalUsage("sim", fmt.Errorf("create: --type <device-type-id> is required"))
	}
	runtime := pf.flags["--runtime"]
	if runtime == "" {
		fatalUsage("sim", fmt.Errorf("create: --runtime <runtime-id> is required"))
	}
	dispatchAndExit("sim_create", map[string]any{
		"name":           pf.positional[0],
		"device_type_id": deviceType,
		"runtime_id":     runtime,
	}, pf.bools["--json"])
}

func runSimBoot(args []string) {
	pf, err := parseFlags(args, nil, nil)
	if err != nil {
		fatalUsage("sim", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("sim", fmt.Errorf("boot: expected <udid>"))
	}
	dispatchAndExit("sim_boot", map[string]any{"udid": pf.positional[0]}, false)
}

func runSimShutdown(args []string) {
	pf, err := parseFlags(args, nil, nil)
	if err != nil {
		fatalUsage("sim", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("sim", fmt.Errorf("shutdown: expected <udid>"))
	}
	dispatchAndExit("sim_shutdown", map[string]any{"udid": pf.positional[0]}, false)
}

func runSimDelete(args []string) {
	pf, err := parseFlags(args, nil, nil)
	if err != nil {
		fatalUsage("sim", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("sim", fmt.Errorf("delete: expected <udid>"))
	}
	dispatchAndExit("sim_delete", map[string]any{"udid": pf.positional[0]}, false)
}

// --- emu subcommands ------------------------------------------------

// runEmu dispatches `spyder emu <subcommand>` for Android emulator lifecycle.
func runEmu(args []string) {
	if len(args) == 0 {
		fatalUsage("emu", fmt.Errorf("missing subcommand — expected list|create|boot|shutdown|delete"))
	}
	switch args[0] {
	case "list":
		runEmuList(args[1:])
	case "create":
		runEmuCreate(args[1:])
	case "boot":
		runEmuBoot(args[1:])
	case "shutdown":
		runEmuShutdown(args[1:])
	case "delete":
		runEmuDelete(args[1:])
	default:
		fatalUsage("emu", fmt.Errorf("unknown subcommand %q — expected list|create|boot|shutdown|delete", args[0]))
	}
}

func runEmuList(args []string) {
	pf, err := parseFlags(args, nil, []string{"--json"})
	if err != nil {
		fatalUsage("emu", err)
	}
	dispatchAndExit("emu_list", map[string]any{}, pf.bools["--json"])
}

func runEmuCreate(args []string) {
	pf, err := parseFlags(args, []string{"--image", "--device"}, []string{"--json"})
	if err != nil {
		fatalUsage("emu", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("emu", fmt.Errorf("create: expected <name> (with --image and --device flags)"))
	}
	image := pf.flags["--image"]
	if image == "" {
		fatalUsage("emu", fmt.Errorf("create: --image <system-image-package> is required"))
	}
	deviceProfile := pf.flags["--device"]
	if deviceProfile == "" {
		fatalUsage("emu", fmt.Errorf("create: --device <device-profile> is required"))
	}
	dispatchAndExit("emu_create", map[string]any{
		"name":           pf.positional[0],
		"system_image":   image,
		"device_profile": deviceProfile,
	}, pf.bools["--json"])
}

func runEmuBoot(args []string) {
	pf, err := parseFlags(args, nil, nil)
	if err != nil {
		fatalUsage("emu", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("emu", fmt.Errorf("boot: expected <avd-name>"))
	}
	dispatchAndExit("emu_boot", map[string]any{"name": pf.positional[0]}, false)
}

func runEmuShutdown(args []string) {
	pf, err := parseFlags(args, nil, nil)
	if err != nil {
		fatalUsage("emu", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("emu", fmt.Errorf("shutdown: expected <serial> (e.g. emulator-5554)"))
	}
	dispatchAndExit("emu_shutdown", map[string]any{"serial": pf.positional[0]}, false)
}

func runEmuDelete(args []string) {
	pf, err := parseFlags(args, nil, nil)
	if err != nil {
		fatalUsage("emu", err)
	}
	if len(pf.positional) != 1 {
		fatalUsage("emu", fmt.Errorf("delete: expected <avd-name>"))
	}
	dispatchAndExit("emu_delete", map[string]any{"name": pf.positional[0]}, false)
}

func runRecord(args []string) {
	pf, err := parseFlags(args, []string{"--as"}, []string{"--start", "--stop"})
	if err != nil {
		fatalUsage("record", err)
	}
	requirePositional("record", pf, 1)
	dev := pf.positional[0]
	start := pf.bools["--start"]
	stop := pf.bools["--stop"]
	if start == stop {
		fatalUsage("record", fmt.Errorf("exactly one of --start or --stop is required"))
	}
	a := map[string]any{"device": dev}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}
	if start {
		dispatchAndExit("record_start", a, false)
	} else {
		dispatchAndExit("record_stop", a, false)
	}
}

// --- helpers --------------------------------------------------------

func fatalUsage(cmd string, err error) {
	fmt.Fprintf(os.Stderr, "spyder %s: %v\n", cmd, err)
	if c := lookupCLI(cmd); c != nil {
		fmt.Fprintf(os.Stderr, "%s\n", c.usage)
	}
	os.Exit(2)
}

// parsePositiveInt parses a positive integer out of a string, failing
// on non-numeric or zero/negative values.
func parsePositiveInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a positive integer: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return 0, fmt.Errorf("must be positive: %q", s)
	}
	return n, nil
}

// sanitizeFilenameComponent replaces path separators in a device
// reference so screenshots land in CWD rather than some surprising
// subdirectory.
func sanitizeFilenameComponent(s string) string {
	s = filepath.Base(s)
	return strings.NewReplacer("/", "_", "\\", "_").Replace(s)
}
