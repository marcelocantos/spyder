// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// spyder is an MCP server for cross-platform mobile development workflow
// orchestration — device inventory, wake/state management, session-aware
// prep/run/restore cycles around tests on real devices.
//
// Usage:
//
//	spyder serve [--addr :3030]  Start the HTTP MCP server
//	spyder run -- <cmd>          Run a command under a device reservation
//	spyder version               Print version and exit
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/marcelocantos/spyder/internal/daemon"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/runs"
)

//go:embed agents-guide.md
var agentsGuide string

// version is set by ldflags at build time.
var version = "dev"

// defaultAddr is the HTTP listen address when `spyder serve` is invoked
// without --addr. Binds to loopback only by default — the MCP endpoint
// exposes powerful tool surfaces (screenshots, app lifecycle, device
// reservation) with no auth, so a wildcard bind on shared Wi-Fi would
// let any LAN peer drive your devices. Pass --addr :3030 explicitly
// to expose externally.
const defaultAddr = "127.0.0.1:3030"

// defaultRunDevice is the device alias used when `spyder run` is invoked
// without --device. The inventory must contain an entry with this alias.
const defaultRunDevice = "Pippa"

const usage = `Usage: spyder [command]

Commands:
  serve         Start the HTTP MCP server (default :3030, endpoints /mcp and /api/v1/)
  run           Run a command under an auto-acquired device reservation
  version       Print version and exit
  help-agent    Print the usage above followed by the agent guide

Device tools (proxy to a running daemon; see SPYDER_DAEMON_URL):
  devices       List connected devices (--platform ios|android|all, --json)
  resolve       Resolve a device alias to platform identifiers
  device-state  Report battery, thermal, foreground app
  screenshot    Capture a PNG to a file (--output FILE, --as OWNER)
  list-apps     List installed third-party apps
  launch-app    Launch an app by bundle id (--as OWNER)
  terminate-app Terminate an app by bundle id (--as OWNER)
  rotate        Rotate a simulator/emulator (--to <orientation>, --as OWNER)
  install       Install a .app/.ipa/.apk on a device (--as OWNER)
  uninstall     Remove an app by bundle id (--as OWNER)
  deploy        Atomic deploy: terminate → install → launch → verify pid (--bundle-id, --as OWNER)
  reserve       Acquire an exclusive lock (--as OWNER, --ttl SECONDS, --note)
  release       Release a reservation you hold (--as OWNER)
  renew         Extend a reservation you hold (--as OWNER, --ttl SECONDS)
  reservations  List all active reservations (--json)
  runs          Inspect run-artefact bundles (list|show|artefacts)
  crashes       Fetch crash reports (--since RFC3339, --process NAME, --as OWNER, --json)
  diff          Compare a screenshot against its stored baseline (--variant, --tolerance, --json)
  baseline      Manage visual baselines; subcommand: update
  record        Start or stop a screen recording (--start | --stop, --as OWNER)
  net           Apply or clear network conditions (--profile NAME | --clear, --as OWNER)
  log           Fetch or tail device logs (--follow for live SSE stream)

Serve:
  spyder serve [--addr :3030]

  Runs an MCP server over streamable HTTP. Register with Claude Code:
    claude mcp add --scope user --transport http spyder http://localhost:3030/mcp

  The same listener exposes REST at http://<host>/api/v1/<tool> (POST JSON).

Run:
  spyder run [--device <alias> | --on PREDICATE] [--timeout DURATION]
             [--as OWNER] -- <command> [args...]

  Executes <command> with its args, waits for it to exit, then releases
  the device reservation. Exits with the command's exit code. Useful for
  wrapping xcodebuild test commands under an auto-acquired reservation.

  --on PREDICATE accepts the same selector grammar as
  'spyder reserve --on PREDICATE' (e.g. platform=ios,os>=17). The daemon
  resolves+acquires atomically — no two-phase race window.

  --timeout DURATION (e.g. 5m, 90s) bounds the wrapped child invocation.
  When the deadline fires, the child is signalled and spyder exits with
  code 30 (timeout) instead of forwarding the child's signal-induced exit.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "serve":
		runServe(os.Args[2:])
	case "run":
		runCmd(os.Args[2:])
	case "version", "--version", "-version":
		fmt.Printf("spyder %s\n", version)
	case "help", "--help", "-help":
		fmt.Print(usage)
	case "help-agent", "--help-agent", "-help-agent":
		fmt.Print(usage)
		fmt.Println()
		fmt.Print(agentsGuide)
	default:
		if c := lookupCLI(cmd); c != nil {
			c.run(os.Args[2:])
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(1)
	}
}

// runServe parses optional --addr and starts the HTTP MCP server.
func runServe(args []string) {
	cfg := daemon.Config{Addr: defaultAddr, Version: version}
	for len(args) > 0 {
		switch args[0] {
		case "--addr", "-a":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "serve: --addr requires a value")
				os.Exit(2)
			}
			cfg.Addr = args[1]
			args = args[2:]
		default:
			fmt.Fprintf(os.Stderr, "serve: unknown flag %q\n", args[0])
			os.Exit(2)
		}
	}
	if err := daemon.Start(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

// runArgs are the parsed CLI flags for `spyder run`.
type runArgs struct {
	Device  string
	On      string        // selector predicate (--on); resolved to a device alias before Acquire
	Owner   string        // empty = derive from filepath.Base(cwd)
	Timeout time.Duration // 0 = no cell budget (current behaviour)
	Command []string
}

// parseRunArgs parses the flag portion of `spyder run`. Extracted so
// it can be unit-tested without touching exec/os.
func parseRunArgs(args []string) (runArgs, error) {
	out := runArgs{}
	deviceSet := false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		switch args[0] {
		case "--device", "-d":
			if len(args) < 2 {
				return runArgs{}, fmt.Errorf("--device requires a value")
			}
			out.Device = args[1]
			deviceSet = true
			args = args[2:]
		case "--on":
			if len(args) < 2 {
				return runArgs{}, fmt.Errorf("--on requires a value")
			}
			out.On = args[1]
			args = args[2:]
		case "--as":
			if len(args) < 2 {
				return runArgs{}, fmt.Errorf("--as requires a value")
			}
			out.Owner = args[1]
			args = args[2:]
		case "--timeout":
			if len(args) < 2 {
				return runArgs{}, fmt.Errorf("--timeout requires a value")
			}
			d, err := time.ParseDuration(args[1])
			if err != nil {
				return runArgs{}, fmt.Errorf("--timeout: %v", err)
			}
			if d <= 0 {
				return runArgs{}, fmt.Errorf("--timeout must be positive, got %s", args[1])
			}
			out.Timeout = d
			args = args[2:]
		default:
			return runArgs{}, fmt.Errorf("unknown flag %q", args[0])
		}
	}
	if out.On != "" && deviceSet {
		return runArgs{}, fmt.Errorf("--device and --on are mutually exclusive")
	}
	if !deviceSet && out.On == "" {
		out.Device = defaultRunDevice
	}
	if len(args) == 0 {
		return runArgs{}, fmt.Errorf("no command provided — usage: spyder run [--device X | --on PREDICATE] [--as OWNER] [--timeout DURATION] -- <cmd> [args...]")
	}
	out.Command = args
	return out, nil
}

// deriveOwner returns the supplied owner or falls back to
// filepath.Base(cwd). Used by `spyder run` to pick a sensible
// project-oriented identity without requiring a flag.
func deriveOwner(supplied string) string {
	if supplied != "" {
		return supplied
	}
	wd, err := os.Getwd()
	if err != nil {
		return "spyder-run"
	}
	return filepath.Base(wd)
}

// runCmd implements the `spyder run [--device X|--on PREDICATE] [--timeout D] -- <cmd> [args]`
// subcommand: exec the command, wait for exit, then release the device
// reservation regardless of success/failure. Exits with the command's
// exit code (or ExitTimeout when --timeout fires before the child exits).
func runCmd(args []string) {
	parsed, err := parseRunArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spyder run: %v\n", err)
		os.Exit(2)
	}

	owner := deriveOwner(parsed.Owner)
	invStore := inventory.New()
	normalize := func(ref string) string {
		if entry, ok := invStore.Lookup(ref); ok && entry.Alias != "" {
			return entry.Alias
		}
		return ref
	}

	if parsed.On != "" {
		alias, resolveErr := resolveSelectorViaDaemon(parsed.On, owner)
		if resolveErr != nil {
			fmt.Fprintf(os.Stderr, "spyder run: --on: %v\n", resolveErr)
			os.Exit(1)
		}
		parsed.Device = alias
	}

	resvStore, resvErr := reservations.New(
		filepath.Join(paths.Base(), "reservations.json"),
		reservations.WithNormalizer(normalize),
	)
	if resvErr != nil {
		fmt.Fprintf(os.Stderr, "spyder run: reservations unavailable: %v\n", resvErr)
		os.Exit(1)
	}
	// runsStore is best-effort — if it fails we still run the command.
	runsStore, runsErr := runs.New(paths.RunsBase())
	if runsErr != nil {
		fmt.Fprintf(os.Stderr, "spyder run: runs store unavailable: %v\n", runsErr)
		runsStore = nil
	}

	note := fmt.Sprintf("spyder run: %s", strings.Join(parsed.Command, " "))
	if _, err := resvStore.Acquire(parsed.Device, owner, reservations.DefaultTTL, note); err != nil {
		if reservations.IsConflict(err) {
			fmt.Fprintf(os.Stderr, "spyder run: %v\n", err)
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "spyder run: %v\n", err)
		os.Exit(1)
	}

	var runID string
	if runsStore != nil {
		canonical := normalize(parsed.Device)
		if r, err := runsStore.Open(canonical, owner, note); err != nil {
			fmt.Fprintf(os.Stderr, "spyder run: open run: %v\n", err)
		} else {
			runID = r.ID
			fmt.Fprintf(os.Stderr, "spyder run: opened run %s\n", runID)
		}
	}

	// Guarantee release on any exit path (success, error, SIGINT).
	// The child inherits our stdio, so a SIGINT on our process group
	// also terminates it before we reach this block.
	release := func() {
		if runsStore != nil && runID != "" {
			if err := runsStore.Close(runID); err != nil {
				fmt.Fprintf(os.Stderr, "spyder run: close run %s: %v\n", runID, err)
			}
		}
		if err := resvStore.Release(parsed.Device, owner); err != nil {
			fmt.Fprintf(os.Stderr, "spyder run: release %s: %v\n", parsed.Device, err)
		}
	}

	// Opportunistic renewal so long-running commands don't expire
	// mid-run. Ticker interval is less than half the TTL for safety.
	// --timeout, when set, bounds the child's lifetime via context
	// cancellation; on timeout we exit ExitTimeout instead of forwarding
	// the child's signal-induced exit code.
	baseCtx, baseCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer baseCancel()
	ctx := baseCtx
	var cancel context.CancelFunc
	if parsed.Timeout > 0 {
		ctx, cancel = context.WithTimeout(baseCtx, parsed.Timeout)
		defer cancel()
	}
	go func() {
		tick := time.NewTicker(reservations.DefaultTTL / 3)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_, _ = resvStore.Renew(parsed.Device, owner, reservations.DefaultTTL)
			}
		}
	}()

	child := exec.CommandContext(ctx, parsed.Command[0], parsed.Command[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	runErr := child.Run()
	exitCode := 0
	if runErr != nil {
		if parsed.Timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "spyder run: timed out after %s\n", parsed.Timeout)
			release()
			os.Exit(30) // cliexit.ExitTimeout
		}
		if ee, ok := errors.AsType[*exec.ExitError](runErr); ok {
			exitCode = ee.ExitCode()
		} else {
			fmt.Fprintf(os.Stderr, "spyder run: %v\n", runErr)
			exitCode = 1
		}
	}
	release()
	os.Exit(exitCode)
}

// resolveSelectorViaDaemon parses --on PREDICATE as a selector predicate,
// reserves a matching device atomically via the daemon's `reserve` REST
// endpoint, and returns the canonical alias. The caller's local store
// will subsequently see the daemon's reservation record (same file) and
// renew/release it through the normal `spyder run` lifecycle.
//
// Returning here means the device is already reserved for owner; if the
// caller fails to call Release, the reservation will expire on TTL.
func resolveSelectorViaDaemon(predicate, owner string) (string, error) {
	// Defined in cli.go — calls /api/v1/reserve with auto-start fallback.
	return reserveViaDaemon(predicate, owner)
}
