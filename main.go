// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// spyder is an MCP server for cross-platform mobile development workflow
// orchestration — device inventory, wake/state management, session-aware
// prep/run/restore cycles around tests on real devices.
//
// Usage:
//
//	spyder serve [--addr :3030]  Start the HTTP MCP server
//	spyder run -- <cmd>          Run a command, then restore KeepAwake
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
	"github.com/marcelocantos/spyder/internal/device"
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
  run           Run a command, then foreground KeepAwake on the device
  version       Print version and exit
  help-agent    Print the usage above followed by the agent guide

Device tools (proxy to a running daemon; see SPYDER_DAEMON_URL):
  devices       List connected devices (--platform ios|android|all, --json)
  resolve       Resolve a device alias to platform identifiers
  device-state  Report battery, thermal, foreground app
  screenshot    Capture a PNG to a file (--output FILE, --as OWNER)
  keepawake     Foreground KeepAwake on an iOS device (--as OWNER)
  list-apps     List installed third-party apps
  launch-app    Launch an app by bundle id (--as OWNER)
  terminate-app Terminate an app by bundle id (--as OWNER)
  reserve       Acquire an exclusive lock (--as OWNER, --ttl SECONDS, --note)
  release       Release a reservation you hold (--as OWNER)
  renew         Extend a reservation you hold (--as OWNER, --ttl SECONDS)
  reservations  List all active reservations (--json)
  runs          Inspect run-artefact bundles (list|show|artefacts)

Serve:
  spyder serve [--addr :3030]

  Runs an MCP server over streamable HTTP. Register with Claude Code:
    claude mcp add --scope user --transport http spyder http://localhost:3030/mcp

  The same listener exposes REST at http://<host>/api/v1/<tool> (POST JSON).

Run:
  spyder run [--device <alias>] -- <command> [args...]

  Executes <command> with its args, waits for it to exit, and then
  foregrounds KeepAwake on the device (default: Pippa). Exits with the
  command's exit code. Useful for wrapping xcodebuild test to restore
  the keep-awake state after the test runner finishes.
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

// runServe parses optional --addr / --tunneld-addr and starts the HTTP
// MCP server.
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
		case "--tunneld-addr":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "serve: --tunneld-addr requires a value")
				os.Exit(2)
			}
			cfg.TunneldAddr = args[1]
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
	Owner   string // empty = derive from filepath.Base(cwd)
	Command []string
}

// parseRunArgs parses the flag portion of `spyder run`. Extracted so
// it can be unit-tested without touching exec/os.
func parseRunArgs(args []string) (runArgs, error) {
	out := runArgs{Device: defaultRunDevice}
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
			args = args[2:]
		case "--as":
			if len(args) < 2 {
				return runArgs{}, fmt.Errorf("--as requires a value")
			}
			out.Owner = args[1]
			args = args[2:]
		default:
			return runArgs{}, fmt.Errorf("unknown flag %q", args[0])
		}
	}
	if len(args) == 0 {
		return runArgs{}, fmt.Errorf("no command provided — usage: spyder run [--device X] [--as OWNER] -- <cmd> [args...]")
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

// runCmd implements the `spyder run [--device X] -- <cmd> [args]`
// subcommand: exec the command, wait for exit, then foreground
// KeepAwake on the device regardless of success/failure. Exits with
// the command's exit code (KeepAwake restore failure is logged but
// does not override the exit code — the test result is authoritative).
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

	// Guarantee release + restore on any exit path (success, error,
	// SIGINT). The child inherits our stdio, so a SIGINT on our
	// process group also terminates it before we reach this block.
	release := func() {
		if runsStore != nil && runID != "" {
			if err := runsStore.Close(runID); err != nil {
				fmt.Fprintf(os.Stderr, "spyder run: close run %s: %v\n", runID, err)
			}
		}
		if err := resvStore.Release(parsed.Device, owner); err != nil {
			fmt.Fprintf(os.Stderr, "spyder run: release %s: %v\n", parsed.Device, err)
		}
		if err := restoreKeepAwake(parsed.Device); err != nil {
			fmt.Fprintf(os.Stderr, "spyder run: restore KeepAwake on %s: %v\n", parsed.Device, err)
		} else {
			fmt.Fprintf(os.Stderr, "spyder run: KeepAwake restored on %s\n", parsed.Device)
		}
	}

	// Opportunistic renewal so long-running commands don't expire
	// mid-run. Ticker interval is less than half the TTL for safety.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
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

// restoreKeepAwake foregrounds the KeepAwake app on the device named
// by the inventory alias (falling back to raw passthrough for
// unknown names).
func restoreKeepAwake(dev string) error {
	store := inventory.New()
	id := dev
	if entry, ok := store.Lookup(dev); ok {
		switch entry.Platform {
		case "ios":
			id = entry.IOSUUID
			if id == "" {
				id = entry.IOSCoreDevice
			}
		case "android":
			return fmt.Errorf("KeepAwake on Android is a no-op (OS-native — enable Settings → Developer options → Stay awake)")
		}
	}
	adapter := device.NewIOSAdapter()
	return adapter.LaunchKeepAwake(id)
}
