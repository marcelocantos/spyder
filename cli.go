// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
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

	"github.com/marcelocantos/spyder/internal/cliexit"
	"github.com/marcelocantos/spyder/internal/clitimeout"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/rest"
	"github.com/marcelocantos/spyder/internal/selector"
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
		{"resolve", "spyder resolve (<name>|--on PREDICATE) [--json]", runResolve},
		{"device-state", "spyder device-state <device> [--json]", runDeviceState},
		{"screenshot", "spyder screenshot <device> [--output FILE] [--as OWNER]", runScreenshot},
		{"list-apps", "spyder list-apps <device> [--json]", runListApps},
		{"launch-app", "spyder launch-app <device> <bundle-id> [--as OWNER]", runLaunchApp},
		{"terminate-app", "spyder terminate-app <device> <bundle-id> [--as OWNER]", runTerminateApp},
		{"is-running", "spyder is-running <device> <bundle-id> [--json]", runIsRunning},
		{"install", "spyder install <device> <path> [--as OWNER]", runInstall},
		{"uninstall", "spyder uninstall <device> <bundle-id> [--as OWNER]", runUninstall},
		{"deploy", "spyder deploy <device> <path> [--bundle-id ID] [--as OWNER]", runDeploy},
		{"reserve", "spyder reserve (<device>|--on PREDICATE|--selector JSON|--platform PLATFORM [--model FAMILY] [--tag TAG]...) [--as OWNER] [--ttl SECONDS] [--note TEXT]", runReserve},
		{"release", "spyder release <device> [--as OWNER]", runRelease},
		{"renew", "spyder renew <device> [--as OWNER] [--ttl SECONDS]", runRenew},
		{"reservations", "spyder reservations [--json]", runReservations},
		{"runs", "spyder runs <list|show|artefacts> [args...]", runRuns},
		{"rotate", "spyder rotate <device> --to <orientation> [--as OWNER]", runRotate},
		{"crashes", "spyder crashes <device> [--since RFC3339] [--process NAME] [--as OWNER] [--json]", runCrashes},
		{"diff", "spyder diff <suite>/<case> <screenshot> [<manifest>] [--variant V] [--tolerance F] [--json]", runDiff},
		{"baseline", "spyder baseline update <suite>/<case> <screenshot> [<manifest>] [--variant V]", runBaseline},
		{"sim", "spyder sim <list|create|boot|shutdown|delete> [args...]", runSim},
		{"emu", "spyder emu <list|create|boot|shutdown|delete> [args...]", runEmu},
		{"record", "spyder record <device> --start | --stop [--as OWNER]", runRecord},
		{"net", "spyder net <device> [--profile NAME | --clear] [--as OWNER]", runNet},
		{"log", "spyder log <device> [--process P] [--subsystem S] [--tag T] [--regex R] [--since TS] [--until TS] [--follow]", runLog},
		{"pool", "spyder pool <list|warm|drain> [args...]", runPool},
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

// daemonError is the structured failure the CLI surfaces from postTool
// when the daemon returns an HTTP error or the transport blows up. It
// carries the bits cliexit.MapDaemonError needs to pick a specific
// exit code (statusCode, errorCode, errorMessage), wrapped in an
// error type so it round-trips through the standard error-return path.
type daemonError struct {
	StatusCode   int    // 0 means transport-level (connection refused, deadline, etc.)
	ErrorCode    string // daemon-side machine code (e.g. "device_not_paired"); "" when the daemon didn't return JSON
	ErrorMessage string // human-readable message (transport text or daemon's "message" field)
}

func (e *daemonError) Error() string { return e.ErrorMessage }

// asDaemonError extracts the *daemonError if err is one (directly or
// wrapped). Returns (nil, false) for plain errors.
func asDaemonError(err error) (*daemonError, bool) {
	return errors.AsType[*daemonError](err)
}

// postTool POSTs args to /api/v1/<tool> on the local daemon and
// returns the parsed result or a *daemonError. Tool errors
// (result.isError=true) are returned in the result; callers decide
// how to surface them. If the first call fails with ECONNREFUSED
// *and* the CLI is targeting the default loopback daemon, it tries
// to spawn a detached `spyder serve` and retry once. Users who set
// SPYDER_DAEMON_URL to a remote daemon get a plain transport error.
func postTool(ctx context.Context, tool string, args map[string]any) (*toolResultContent, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode args: %w", err)
	}
	base := daemonBaseURL()
	url := base + rest.Prefix + tool
	resp, err := postWithCtx(ctx, url, body)
	if err != nil && isConnRefused(err) && base == defaultDaemonURL {
		if spawnErr := autoStartDaemon(); spawnErr != nil {
			return nil, &daemonError{
				ErrorMessage: fmt.Sprintf("daemon not reachable at %s and auto-start failed: %v — try `brew services start spyder`",
					base, spawnErr),
			}
		}
		resp, err = postWithCtx(ctx, url, body)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &daemonError{ErrorMessage: fmt.Sprintf("request timed out: %v", err)}
		}
		if isConnRefused(err) {
			return nil, &daemonError{
				ErrorMessage: fmt.Sprintf("daemon not reachable at %s — start it with `brew services start spyder` or `spyder serve`",
					base),
			}
		}
		return nil, &daemonError{ErrorMessage: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// Daemon's REST error body is `{"error":"<code>","message":"<text>"}`.
		// Decode it so the exit-code mapper can branch on the structured code.
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &body)
		msg := body.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return nil, &daemonError{
			StatusCode:   resp.StatusCode,
			ErrorCode:    body.Error,
			ErrorMessage: msg,
		}
	}
	var out toolResultContent
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &daemonError{ErrorMessage: fmt.Sprintf("parse daemon response: %v (body: %s)", err, raw)}
	}
	return &out, nil
}

// postWithCtx is http.Post with a context. Extracted so the retry path
// can share the same helper.
func postWithCtx(ctx context.Context, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
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

// setupCommand is the shared boilerplate every CLI subcommand runs
// before dispatch:
//
//   - injects a `--timeout DURATION` flag on top of the caller-supplied
//     stringFlags list, defaulting to defaultTimeout;
//   - injects `-v` and `--verbose` bool flags on top of the caller-
//     supplied boolFlags list (consumed by mutating commands to gate
//     post-success output — see verbose() and dispatchAndExit's
//     quietOnSuccess parameter);
//   - parses argv via parseFlags, exiting with cliexit.ExitUsage on
//     malformed flags (so callers don't have to repeat `if err != nil
//     { fatalUsage(...) }`);
//   - parses the resolved --timeout value (or default);
//   - returns a context bounded by that timeout (or context.Background
//     for timeout == 0), plus its cancel function — caller defers cancel.
//
// 🎯T37.1 + 🎯T37.3 + 🎯T37.5: every subcommand goes through this so
// the --timeout, --verbose/-v flags, the per-command default, and the
// timeout exit-code path are uniform across the surface.
func setupCommand(
	name string,
	args []string,
	stringFlags, boolFlags []string,
	defaultTimeout time.Duration,
) (parsedFlags, context.Context, context.CancelFunc) {
	stringFlags = append(stringFlags, "--timeout")
	boolFlags = append(boolFlags, "-v", "--verbose")
	pf, err := parseFlags(args, stringFlags, boolFlags)
	if err != nil {
		fatalUsage(name, err)
	}
	timeout := defaultTimeout
	if v := pf.flags["--timeout"]; v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil {
			fatalUsage(name, fmt.Errorf("--timeout: %v", perr))
		}
		timeout = d
	}
	ctx, cancel := clitimeout.Context(timeout)
	return pf, ctx, cancel
}

// verbose returns true when the user passed -v or --verbose on this
// invocation. Used by mutating commands to decide whether to surface
// the daemon's confirmation text on success (default: silent).
func verbose(pf parsedFlags) bool {
	return pf.bools["-v"] || pf.bools["--verbose"]
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
// tool error. `jsonMode` prints the first text block verbatim (handlers
// that return structured data already produce JSON). `quietOnSuccess`
// suppresses non-error output entirely — used by mutating commands so
// the script-friendly default is "silent on success, exit 0".
//
// On a tool error the daemon's structured error code lives inside the
// text block (best-effort prose match via cliexit.MapDaemonError); the
// HTTP status is 200 even for tool errors because the JSON-RPC layer
// wraps them. So we pass statusCode=0 to MapDaemonError and let the
// prose path classify. Errors always print regardless of
// quietOnSuccess.
func renderResult(r *toolResultContent, jsonMode, quietOnSuccess bool) {
	text := r.firstText()
	if r.IsError {
		code := cliexit.MapDaemonError(0, "", text)
		cliexit.Errorf(code, "%s", text)
	}
	if quietOnSuccess {
		return
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
		msg := fmt.Sprintf("%s: expected %d positional arg(s), got %d", name, n, len(pf.positional))
		if cmd != nil {
			msg += "\n" + cmd.usage
		}
		cliexit.Errorf(cliexit.ExitUsage, "%s", msg)
	}
}

// dispatchAndExit runs postTool, prints the result, and exits. Failure
// modes route through cliexit:
//
//   - daemonError → cliexit.MapDaemonError picks a code from the
//     structured fields (ExitDeviceNotConnected, ExitAppNotInstalled,
//     ExitTimeout, …);
//   - plain error → ExitGeneric.
//   - tool-error result → renderResult does the same mapping over the
//     prose body.
//
// quietOnSuccess gates post-success output. Pass false for read
// commands (the response IS the data the caller asked for); pass
// !verbose(pf) for mutating commands so success defaults to silent
// and -v restores the daemon's confirmation text.
func dispatchAndExit(ctx context.Context, tool string, args map[string]any, jsonMode, quietOnSuccess bool) {
	res, err := postTool(ctx, tool, args)
	if err != nil {
		cliexit.Errorf(daemonExitCode(err), "spyder %s: %v", tool, err)
	}
	renderResult(res, jsonMode, quietOnSuccess)
}

// daemonExitCode maps a postTool error to a cliexit code. *daemonError
// goes through cliexit.MapDaemonError; everything else is generic.
func daemonExitCode(err error) int {
	if de, ok := asDaemonError(err); ok {
		return cliexit.MapDaemonError(de.StatusCode, de.ErrorCode, de.ErrorMessage)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return cliexit.ExitTimeout
	}
	return cliexit.ExitGeneric
}

// reserveViaDaemon parses an --on predicate, posts to /api/v1/reserve so
// the daemon resolves+acquires atomically, and returns the canonical
// alias from the response. Used by `spyder run --on PREDICATE` to close
// the resolve→release→re-acquire race window in the previous workaround
// pattern (🎯T38).
func reserveViaDaemon(predicate, owner string) (string, error) {
	sel, perr := selector.ParseSelectorString(predicate)
	if perr != nil {
		return "", fmt.Errorf("parse selector: %w", perr)
	}
	selBytes, merr := json.Marshal(sel)
	if merr != nil {
		return "", fmt.Errorf("marshal selector: %w", merr)
	}
	args := map[string]any{
		"selector": string(selBytes),
		"owner":    owner,
		"note":     "spyder run --on",
	}
	ctx, cancel := clitimeout.Context(clitimeout.DefaultReserve)
	defer cancel()
	res, err := postTool(ctx, "reserve", args)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", errors.New(res.firstText())
	}
	var reservation struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal([]byte(res.firstText()), &reservation); err != nil {
		return "", fmt.Errorf("parse reserve response: %w", err)
	}
	if reservation.Device == "" {
		return "", errors.New("daemon returned empty device alias")
	}
	return reservation.Device, nil
}

// --- subcommand implementations -------------------------------------

func runDevices(args []string) {
	pf, ctx, cancel := setupCommand("devices", args, []string{"--platform"}, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	a := map[string]any{}
	if p := pf.flags["--platform"]; p != "" {
		a["platform"] = p
	}
	dispatchAndExit(ctx, "devices", a, pf.bools["--json"], false)
}

func runResolve(args []string) {
	pf, ctx, cancel := setupCommand("resolve", args, []string{"--on"}, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()

	onPredicate := pf.flags["--on"]
	switch {
	case onPredicate != "" && len(pf.positional) > 0:
		fatalUsage("resolve", fmt.Errorf("supply either a positional name or --on PREDICATE, not both"))
	case onPredicate != "":
		sel, perr := selector.ParseSelectorString(onPredicate)
		if perr != nil {
			cliexit.Errorf(cliexit.ExitSelectorNotSupported,
				"spyder resolve: --on: %v", perr)
		}
		selBytes, merr := json.Marshal(sel)
		if merr != nil {
			cliexit.Errorf(cliexit.ExitGeneric,
				"spyder resolve: marshal selector: %v", merr)
		}
		dispatchAndExit(ctx, "resolve",
			map[string]any{"selector": string(selBytes)},
			pf.bools["--json"], false)
	case len(pf.positional) == 1:
		name := pf.positional[0]
		// 🎯T38.3: distinguish three cases:
		//  1. Known alias (or raw UUID matching a known device) → daemon
		//     resolve with `name`, return inventory entry, exit 0.
		//  2. Predicate (contains '=' or other selector grammar) → parse;
		//     resolve via daemon's selector path, exit 0 on match. Bad
		//     parse → exit 15.
		//  3. Neither alias nor parseable predicate → exit 15. Distinct
		//     from exit 1 so scripts can fall through to platform-specific
		//     tooling rather than retrying. The previous echo-back behavior
		//     (synthetic android-serial classification) silently
		//     misclassified arbitrary strings; that path is gone for the
		//     CLI surface (the MCP `resolve` tool retains it for legacy
		//     callers — see STABILITY.md).
		invStore := inventory.New()
		if _, ok := invStore.Lookup(name); ok {
			dispatchAndExit(ctx, "resolve",
				map[string]any{"name": name},
				pf.bools["--json"], false)
			return
		}
		if looksLikeSelector(name) {
			sel, perr := selector.ParseSelectorString(name)
			if perr != nil {
				cliexit.Errorf(cliexit.ExitSelectorNotSupported,
					"spyder resolve: %q is not a known alias and not a parseable selector predicate: %v",
					name, perr)
			}
			selBytes, merr := json.Marshal(sel)
			if merr != nil {
				cliexit.Errorf(cliexit.ExitGeneric,
					"spyder resolve: marshal selector: %v", merr)
			}
			dispatchAndExit(ctx, "resolve",
				map[string]any{"selector": string(selBytes)},
				pf.bools["--json"], false)
			return
		}
		cliexit.Errorf(cliexit.ExitSelectorNotSupported,
			"spyder resolve: %q is not a known alias and not a parseable selector predicate (no '=' found)",
			name)
	default:
		fatalUsage("resolve", fmt.Errorf("supply a name (positional) or --on PREDICATE"))
	}
}

// looksLikeSelector returns true when s contains a selector-grammar
// separator ('=' or comma between key/value tokens). Aliases are
// short identifiers with no '=' so this heuristic doesn't false-match.
func looksLikeSelector(s string) bool {
	return strings.ContainsAny(s, "=")
}

func runDeviceState(args []string) {
	pf, ctx, cancel := setupCommand("device-state", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	requirePositional("device-state", pf, 1)
	dispatchAndExit(ctx, "device_state",
		map[string]any{"device": pf.positional[0]},
		pf.bools["--json"], false)
}

func runScreenshot(args []string) {
	pf, ctx, cancel := setupCommand("screenshot", args, []string{"--output", "--as"}, nil, clitimeout.DefaultScreenshot)
	defer cancel()
	requirePositional("screenshot", pf, 1)
	dev := pf.positional[0]
	a := map[string]any{"device": dev}
	if o := pf.flags["--as"]; o != "" {
		a["owner"] = o
	} else {
		a["owner"] = deriveOwner("")
	}

	res, err := postTool(ctx, "screenshot", a)
	if err != nil {
		cliexit.Errorf(daemonExitCode(err), "spyder screenshot: %v", err)
	}
	if res.IsError {
		text := res.firstText()
		cliexit.Errorf(cliexit.MapDaemonError(0, "", text), "%s", text)
	}
	png, mime, ok := res.firstImage()
	if !ok {
		cliexit.Errorf(cliexit.ExitGeneric, "spyder screenshot: no image in response")
	}
	output := pf.flags["--output"]
	if output == "" {
		output = fmt.Sprintf("%s-%s.png",
			sanitizeFilenameComponent(dev),
			time.Now().Format("20060102-150405"))
	}
	if err := os.WriteFile(output, png, 0o644); err != nil {
		cliexit.Errorf(cliexit.ExitGeneric, "spyder screenshot: writing %s: %v", output, err)
	}
	// Screenshot's true output is the file. Print the path on stdout
	// so scripts can capture it (`OUT=$(spyder screenshot Pippa)`); the
	// human-readable size+mime line goes to stderr only under -v.
	fmt.Println(output)
	if verbose(pf) {
		fmt.Fprintf(os.Stderr, "wrote %s (%d bytes, %s)\n", output, len(png), mime)
	}
}

func runListApps(args []string) {
	pf, ctx, cancel := setupCommand("list-apps", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	requirePositional("list-apps", pf, 1)
	dispatchAndExit(ctx, "list_apps",
		map[string]any{"device": pf.positional[0]},
		pf.bools["--json"], false)
}

func runLaunchApp(args []string) {
	pf, ctx, cancel := setupCommand("launch-app", args, []string{"--as"}, nil, clitimeout.DefaultLaunch)
	defer cancel()
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
	dispatchAndExit(ctx, "launch_app", a, false, !verbose(pf))
}

func runIsRunning(args []string) {
	pf, ctx, cancel := setupCommand("is-running", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	requirePositional("is-running", pf, 2)

	res, err := postTool(ctx, "is_running", map[string]any{
		"device":    pf.positional[0],
		"bundle_id": pf.positional[1],
	})
	if err != nil {
		cliexit.Errorf(daemonExitCode(err), "spyder is-running: %v", err)
	}
	text := res.firstText()
	if res.IsError {
		cliexit.Errorf(cliexit.MapDaemonError(0, "", text), "%s", text)
	}

	var out struct {
		State string `json:"state"`
		PID   int    `json:"pid,omitempty"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		cliexit.Errorf(cliexit.ExitGeneric, "spyder is-running: parse: %v", err)
	}

	if pf.bools["--json"] {
		fmt.Println(text)
	}

	switch out.State {
	case "running":
		if !pf.bools["--json"] {
			fmt.Printf("running pid=%d\n", out.PID)
		}
		os.Exit(cliexit.ExitOK)
	case "not_running":
		if !pf.bools["--json"] {
			fmt.Println("not running")
		}
		os.Exit(cliexit.ExitAppNotRunning)
	case "not_installed":
		if !pf.bools["--json"] {
			fmt.Println("not installed")
		}
		os.Exit(cliexit.ExitAppNotInstalled)
	default:
		cliexit.Errorf(cliexit.ExitGeneric, "spyder is-running: unexpected state %q", out.State)
	}
}

func runTerminateApp(args []string) {
	pf, ctx, cancel := setupCommand("terminate-app", args, []string{"--as"}, nil, clitimeout.DefaultLaunch)
	defer cancel()
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
	dispatchAndExit(ctx, "terminate_app", a, false, !verbose(pf))
}

func runInstall(args []string) {
	pf, ctx, cancel := setupCommand("install", args, []string{"--as"}, nil, clitimeout.DefaultInstall)
	defer cancel()
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
	dispatchAndExit(ctx, "install_app", a, false, !verbose(pf))
}

func runUninstall(args []string) {
	pf, ctx, cancel := setupCommand("uninstall", args, []string{"--as"}, nil, clitimeout.DefaultInstall)
	defer cancel()
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
	dispatchAndExit(ctx, "uninstall_app", a, false, !verbose(pf))
}

func runDeploy(args []string) {
	pf, ctx, cancel := setupCommand("deploy", args, []string{"--as", "--bundle-id"}, nil, clitimeout.DefaultDeploy)
	defer cancel()
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
	dispatchAndExit(ctx, "deploy_app", a, false, !verbose(pf))
}

func runReserve(args []string) {
	// --tag may be repeated; parseFlags only handles single-value flags, so
	// we pre-scan for --tag values before passing to parseFlags.
	var tagValues []string
	filteredArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--tag" {
			if i+1 >= len(args) {
				fatalUsage("reserve", fmt.Errorf("--tag requires a value"))
			}
			tagValues = append(tagValues, args[i+1])
			i++
			continue
		}
		filteredArgs = append(filteredArgs, args[i])
	}

	pf, ctx, cancel := setupCommand("reserve", filteredArgs,
		[]string{"--as", "--ttl", "--note", "--selector", "--on", "--platform", "--model"},
		nil,
		clitimeout.DefaultReserve,
	)
	defer cancel()

	a := map[string]any{}
	a["owner"] = deriveOwner(pf.flags["--as"])

	// Determine reservation target: positional device, --on PREDICATE,
	// --selector JSON, or shorthand flags. Mutually exclusive.
	selectorJSON := pf.flags["--selector"]
	onPredicate := pf.flags["--on"]
	platform := pf.flags["--platform"]
	model := pf.flags["--model"]

	literalDevice := ""
	if len(pf.positional) > 0 {
		literalDevice = pf.positional[0]
	}

	hasShorthand := platform != "" || len(tagValues) > 0 || model != ""
	hasSelectorish := selectorJSON != "" || onPredicate != "" || hasShorthand

	switch {
	case literalDevice != "" && hasSelectorish:
		fatalUsage("reserve", fmt.Errorf("supply either a positional device or selector flags, not both"))

	case selectorJSON != "" && (onPredicate != "" || hasShorthand):
		fatalUsage("reserve", fmt.Errorf("--selector and other selector flags (--on, --platform, --model, --tag) are mutually exclusive"))

	case onPredicate != "" && hasShorthand:
		fatalUsage("reserve", fmt.Errorf("--on and shorthand flags (--platform, --model, --tag) are mutually exclusive"))

	case literalDevice != "":
		a["device"] = literalDevice

	case onPredicate != "":
		// 🎯T37.4: parse --on PREDICATE into a Selector and marshal as JSON.
		sel, perr := selector.ParseSelectorString(onPredicate)
		if perr != nil {
			fatalUsage("reserve", fmt.Errorf("--on: %v", perr))
		}
		selBytes, merr := json.Marshal(sel)
		if merr != nil {
			fatalUsage("reserve", fmt.Errorf("--on: marshal: %v", merr))
		}
		a["selector"] = string(selBytes)

	case selectorJSON != "":
		a["selector"] = selectorJSON

	case hasShorthand:
		if platform == "" {
			fatalUsage("reserve", fmt.Errorf("--platform is required when using selector shorthand flags"))
		}
		sel := map[string]any{"platform": platform}
		if model != "" {
			sel["model_family"] = model
		}
		if len(tagValues) > 0 {
			sel["tags"] = tagValues
		}
		selBytes, merr := json.Marshal(sel)
		if merr != nil {
			fatalUsage("reserve", fmt.Errorf("building selector: %v", merr))
		}
		a["selector"] = string(selBytes)

	default:
		fatalUsage("reserve", fmt.Errorf("supply a device (positional), --on PREDICATE, --selector JSON, or --platform/--model/--tag"))
	}

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
	dispatchAndExit(ctx, "reserve", a, false, !verbose(pf))
}

func runRelease(args []string) {
	pf, ctx, cancel := setupCommand("release", args, []string{"--as"}, nil, clitimeout.DefaultReserve)
	defer cancel()
	requirePositional("release", pf, 1)
	a := map[string]any{
		"device": pf.positional[0],
		"owner":  deriveOwner(pf.flags["--as"]),
	}
	dispatchAndExit(ctx, "release", a, false, !verbose(pf))
}

func runRenew(args []string) {
	pf, ctx, cancel := setupCommand("renew", args, []string{"--as", "--ttl"}, nil, clitimeout.DefaultReserve)
	defer cancel()
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
	dispatchAndExit(ctx, "renew", a, false, !verbose(pf))
}

func runReservations(args []string) {
	pf, ctx, cancel := setupCommand("reservations", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	dispatchAndExit(ctx, "reservations", map[string]any{}, pf.bools["--json"], false)
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
	pf, ctx, cancel := setupCommand("runs", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	dispatchAndExit(ctx, "runs_list", map[string]any{}, pf.bools["--json"], false)
}

func runRunsShow(args []string) {
	pf, ctx, cancel := setupCommand("runs", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("runs", fmt.Errorf("show expects one run-id"))
	}
	dispatchAndExit(ctx, "runs_show",
		map[string]any{"run_id": pf.positional[0]},
		pf.bools["--json"], false)
}

// runRunsArtefacts reuses runs_show and extracts just the artefacts
// array so scripts can pipe it. Defaults to a tabular render; --json
// emits the raw array.
func runRunsArtefacts(args []string) {
	pf, ctx, cancel := setupCommand("runs", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("runs", fmt.Errorf("artefacts expects one run-id"))
	}
	res, err := postTool(ctx, "runs_show", map[string]any{"run_id": pf.positional[0]})
	if err != nil {
		cliexit.Errorf(daemonExitCode(err), "spyder runs artefacts: %v", err)
	}
	if res.IsError {
		text := res.firstText()
		cliexit.Errorf(cliexit.MapDaemonError(0, "", text), "%s", text)
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
		cliexit.Errorf(cliexit.ExitGeneric, "spyder runs artefacts: parse: %v", err)
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
	pf, ctx, cancel := setupCommand("rotate", args, []string{"--to", "--as"}, nil, clitimeout.DefaultLaunch)
	defer cancel()
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
	dispatchAndExit(ctx, "rotate", a, false, !verbose(pf))
}

func runCrashes(args []string) {
	pf, ctx, cancel := setupCommand("crashes", args, []string{"--since", "--process", "--as"}, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
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
	dispatchAndExit(ctx, "crashes", a, pf.bools["--json"], false)
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
	pf, ctx, cancel := setupCommand("sim", args, []string{"--state"}, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	a := map[string]any{}
	if s := pf.flags["--state"]; s != "" {
		a["state"] = s
	}
	dispatchAndExit(ctx, "sim_list", a, pf.bools["--json"], false)
}

func runSimCreate(args []string) {
	pf, ctx, cancel := setupCommand("sim", args, []string{"--type", "--runtime"}, []string{"--json"}, clitimeout.DefaultLaunch)
	defer cancel()
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
	dispatchAndExit(ctx, "sim_create", map[string]any{
		"name":           pf.positional[0],
		"device_type_id": deviceType,
		"runtime_id":     runtime,
	}, pf.bools["--json"], false)
}

func runSimBoot(args []string) {
	pf, ctx, cancel := setupCommand("sim", args, nil, nil, clitimeout.DefaultLaunch)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("sim", fmt.Errorf("boot: expected <udid>"))
	}
	dispatchAndExit(ctx, "sim_boot", map[string]any{"udid": pf.positional[0]}, false, !verbose(pf))
}

func runSimShutdown(args []string) {
	pf, ctx, cancel := setupCommand("sim", args, nil, nil, clitimeout.DefaultLaunch)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("sim", fmt.Errorf("shutdown: expected <udid>"))
	}
	dispatchAndExit(ctx, "sim_shutdown", map[string]any{"udid": pf.positional[0]}, false, !verbose(pf))
}

func runSimDelete(args []string) {
	pf, ctx, cancel := setupCommand("sim", args, nil, nil, clitimeout.DefaultLaunch)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("sim", fmt.Errorf("delete: expected <udid>"))
	}
	dispatchAndExit(ctx, "sim_delete", map[string]any{"udid": pf.positional[0]}, false, !verbose(pf))
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
	pf, ctx, cancel := setupCommand("emu", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	dispatchAndExit(ctx, "emu_list", map[string]any{}, pf.bools["--json"], false)
}

func runEmuCreate(args []string) {
	pf, ctx, cancel := setupCommand("emu", args, []string{"--image", "--device"}, []string{"--json"}, clitimeout.DefaultLaunch)
	defer cancel()
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
	dispatchAndExit(ctx, "emu_create", map[string]any{
		"name":           pf.positional[0],
		"system_image":   image,
		"device_profile": deviceProfile,
	}, pf.bools["--json"], false)
}

func runEmuBoot(args []string) {
	pf, ctx, cancel := setupCommand("emu", args, nil, nil, clitimeout.DefaultLaunch)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("emu", fmt.Errorf("boot: expected <avd-name>"))
	}
	dispatchAndExit(ctx, "emu_boot", map[string]any{"name": pf.positional[0]}, false, !verbose(pf))
}

func runEmuShutdown(args []string) {
	pf, ctx, cancel := setupCommand("emu", args, nil, nil, clitimeout.DefaultLaunch)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("emu", fmt.Errorf("shutdown: expected <serial> (e.g. emulator-5554)"))
	}
	dispatchAndExit(ctx, "emu_shutdown", map[string]any{"serial": pf.positional[0]}, false, !verbose(pf))
}

func runEmuDelete(args []string) {
	pf, ctx, cancel := setupCommand("emu", args, nil, nil, clitimeout.DefaultLaunch)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("emu", fmt.Errorf("delete: expected <avd-name>"))
	}
	dispatchAndExit(ctx, "emu_delete", map[string]any{"name": pf.positional[0]}, false, !verbose(pf))
}

func runRecord(args []string) {
	pf, ctx, cancel := setupCommand("record", args, []string{"--as"}, []string{"--start", "--stop"}, clitimeout.DefaultRecord)
	defer cancel()
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
		dispatchAndExit(ctx, "record_start", a, false, !verbose(pf))
	} else {
		dispatchAndExit(ctx, "record_stop", a, false, !verbose(pf))
	}
}

func runNet(args []string) {
	pf, ctx, cancel := setupCommand("net", args, []string{"--profile", "--as"}, []string{"--clear"}, clitimeout.DefaultLaunch)
	defer cancel()
	requirePositional("net", pf, 1)
	dev := pf.positional[0]
	profile := pf.flags["--profile"]
	clear := pf.bools["--clear"]

	if profile == "" && !clear {
		fatalUsage("net", fmt.Errorf("supply --profile NAME or --clear"))
	}
	if profile != "" && clear {
		fatalUsage("net", fmt.Errorf("--profile and --clear are mutually exclusive"))
	}

	a := map[string]any{
		"device": dev,
		"owner":  deriveOwner(pf.flags["--as"]),
	}
	if clear {
		a["clear"] = true
	} else {
		a["profile"] = profile
	}
	dispatchAndExit(ctx, "network", a, false, !verbose(pf))
}

func runLog(args []string) {
	// `log` has two modes: bounded range query (DefaultRead) and live
	// follow (DefaultLogStream = 0, no timeout). Pick the per-mode
	// default after parsing — setupCommand only sets one ceiling, and
	// the user-supplied --timeout always wins anyway. Use DefaultRead
	// here; the live-follow mode replaces the context below if no
	// explicit --timeout was passed.
	pf, ctx, cancel := setupCommand("log", args,
		[]string{"--process", "--subsystem", "--tag", "--regex", "--since", "--until"},
		[]string{"--follow", "--json"},
		clitimeout.DefaultRead,
	)
	defer cancel()
	requirePositional("log", pf, 1)

	dev := pf.positional[0]
	follow := pf.bools["--follow"]
	jsonMode := pf.bools["--json"]

	if follow {
		// Live follow: drop the read-timeout unless the user explicitly
		// passed --timeout. We can't tell from the parsed flags alone
		// whether --timeout was set, so re-derive: if the supplied flag
		// value is empty, restart with DefaultLogStream.
		if pf.flags["--timeout"] == "" {
			cancel()
			ctx, cancel = clitimeout.Context(clitimeout.DefaultLogStream)
			defer cancel()
		}
		// SSE live stream: POST to /api/v1/log_stream, print each event.
		body := map[string]any{"device": dev}
		if p := pf.flags["--process"]; p != "" {
			body["process"] = p
		}
		if s := pf.flags["--subsystem"]; s != "" {
			body["subsystem"] = s
		}
		if t := pf.flags["--tag"]; t != "" {
			body["tag"] = t
		}
		if r := pf.flags["--regex"]; r != "" {
			body["regex"] = r
		}
		streamSSELog(ctx, body, jsonMode)
		return
	}

	// Bounded range query: POST to /api/v1/logs.
	a := map[string]any{"device": dev}
	for _, flag := range []string{"--process", "--subsystem", "--tag", "--regex", "--since", "--until"} {
		if v := pf.flags[flag]; v != "" {
			key := strings.TrimPrefix(flag, "--")
			a[key] = v
		}
	}
	dispatchAndExit(ctx, "logs", a, jsonMode, false)
}

// streamSSELog POSTs to the SSE log_stream endpoint and prints each
// event line. Errors route through cliexit so live-stream failures use
// the same exit-code map as the rest of the CLI.
func streamSSELog(ctx context.Context, body map[string]any, jsonMode bool) {
	encoded, err := json.Marshal(body)
	if err != nil {
		cliexit.Errorf(cliexit.ExitGeneric, "spyder log: encode: %v", err)
	}
	base := daemonBaseURL()
	url := base + rest.StreamPath
	resp, err := postWithCtx(ctx, url, encoded)
	if err != nil && isConnRefused(err) && base == defaultDaemonURL {
		if spawnErr := autoStartDaemon(); spawnErr == nil {
			resp, err = postWithCtx(ctx, url, encoded)
		}
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			cliexit.Errorf(cliexit.ExitTimeout, "spyder log: request timed out: %v", err)
		}
		if isConnRefused(err) {
			cliexit.Errorf(cliexit.ExitDaemonUnreachable, "spyder log: daemon not reachable at %s", base)
		}
		cliexit.Errorf(cliexit.ExitGeneric, "spyder log: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		var dbody struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &dbody)
		msg := dbody.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		code := cliexit.MapDaemonError(resp.StatusCode, dbody.Error, msg)
		cliexit.Errorf(code, "spyder log: %s", msg)
	}

	// Read SSE events line by line. Each event ends with a blank line.
	// Lines starting with "data: " carry the JSON payload.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if jsonMode {
			fmt.Println(data)
			continue
		}
		// Pretty-print the LogLine fields.
		var ll struct {
			Timestamp string `json:"timestamp"`
			Process   string `json:"process"`
			Level     string `json:"level"`
			Tag       string `json:"tag"`
			Message   string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &ll); err != nil {
			fmt.Println(data)
			continue
		}
		parts := []string{ll.Timestamp}
		if ll.Process != "" {
			parts = append(parts, ll.Process)
		} else if ll.Tag != "" {
			parts = append(parts, ll.Tag)
		}
		if ll.Level != "" {
			parts = append(parts, "["+ll.Level+"]")
		}
		parts = append(parts, ll.Message)
		fmt.Println(strings.Join(parts, " "))
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		cliexit.Errorf(cliexit.ExitGeneric, "spyder log: read: %v", err)
	}
}

// --- pool subcommands -----------------------------------------------

// runPool dispatches `spyder pool <subcommand>` for sim/emu pool management.
func runPool(args []string) {
	if len(args) == 0 {
		fatalUsage("pool", fmt.Errorf("missing subcommand — expected list|warm|drain"))
	}
	switch args[0] {
	case "list":
		runPoolList(args[1:])
	case "warm":
		runPoolWarm(args[1:])
	case "drain":
		runPoolDrain(args[1:])
	default:
		fatalUsage("pool", fmt.Errorf("unknown subcommand %q — expected list|warm|drain", args[0]))
	}
}

func runPoolList(args []string) {
	pf, ctx, cancel := setupCommand("pool", args, nil, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	dispatchAndExit(ctx, "pool_list", map[string]any{}, pf.bools["--json"], false)
}

func runPoolWarm(args []string) {
	pf, ctx, cancel := setupCommand("pool", args, []string{"--count"}, nil, clitimeout.DefaultLaunch)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("pool", fmt.Errorf("warm: expected <template>"))
	}
	a := map[string]any{"template": pf.positional[0]}
	countStr := pf.flags["--count"]
	if countStr == "" {
		countStr = "1"
	}
	n, perr := parsePositiveInt(countStr)
	if perr != nil {
		fatalUsage("pool", fmt.Errorf("--count: %v", perr))
	}
	a["count"] = n
	dispatchAndExit(ctx, "pool_warm", a, false, !verbose(pf))
}

func runPoolDrain(args []string) {
	pf, ctx, cancel := setupCommand("pool", args, nil, nil, clitimeout.DefaultLaunch)
	defer cancel()
	if len(pf.positional) != 1 {
		fatalUsage("pool", fmt.Errorf("drain: expected <template>"))
	}
	dispatchAndExit(ctx, "pool_drain", map[string]any{"template": pf.positional[0]}, false, !verbose(pf))
}

// --- helpers --------------------------------------------------------

func fatalUsage(cmd string, err error) {
	msg := fmt.Sprintf("spyder %s: %v", cmd, err)
	if c := lookupCLI(cmd); c != nil {
		msg += "\n" + c.usage
	}
	cliexit.Errorf(cliexit.ExitUsage, "%s", msg)
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
