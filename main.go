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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/marcelocantos/spyder/internal/daemon"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
)

// version is set by ldflags at build time.
var version = "dev"

// defaultAddr is the HTTP listen address when `spyder serve` is invoked
// without --addr.
const defaultAddr = ":3030"

// defaultRunDevice is the device alias used when `spyder run` is invoked
// without --device. The inventory must contain an entry with this alias.
const defaultRunDevice = "Pippa"

const usage = `Usage: spyder [command]

Commands:
  serve     Start the HTTP MCP server (default :3030, endpoint /mcp)
  run       Run a command, then foreground KeepAwake on the device
  version   Print version and exit

Serve:
  spyder serve [--addr :3030]

  Runs an MCP server over streamable HTTP. Register with Claude Code:
    claude mcp add --scope user --transport http spyder http://localhost:3030/mcp

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
	default:
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

// runCmd implements the `spyder run [--device X] -- <cmd> [args]`
// subcommand: exec the command, wait for exit, then foreground
// KeepAwake on the device regardless of success/failure. Exits with
// the command's exit code (KeepAwake restore failure is logged but
// does not override the exit code — the test result is authoritative).
func runCmd(args []string) {
	dev := defaultRunDevice
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		switch args[0] {
		case "--device", "-d":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "spyder run: --device requires a value")
				os.Exit(2)
			}
			dev = args[1]
			args = args[2:]
		default:
			fmt.Fprintf(os.Stderr, "spyder run: unknown flag %q\n", args[0])
			os.Exit(2)
		}
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "spyder run: no command provided — usage: spyder run [--device X] -- <cmd> [args...]")
		os.Exit(2)
	}

	child := exec.Command(args[0], args[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	runErr := child.Run()
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			fmt.Fprintf(os.Stderr, "spyder run: %v\n", runErr)
			exitCode = 1
		}
	}

	if err := restoreKeepAwake(dev); err != nil {
		fmt.Fprintf(os.Stderr, "spyder run: restore KeepAwake on %s: %v\n", dev, err)
	} else {
		fmt.Fprintf(os.Stderr, "spyder run: KeepAwake restored on %s\n", dev)
	}

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
