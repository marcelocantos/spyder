// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// spyder is an MCP server for cross-platform mobile development workflow
// orchestration — device inventory, wake/state management, session-aware
// prep/run/restore cycles around tests on real devices.
//
// Usage:
//
//	spyder             MCP stdio proxy — auto-starts daemon if needed
//	spyder serve       Start the persistent background daemon
//	spyder version     Print version and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/marcelocantos/mcpbridge"

	"github.com/marcelocantos/spyder/internal/daemon"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/paths"
)

// defaultRunDevice is the device alias used when `spyder run` is invoked
// without --device. The inventory must contain an entry with this alias.
const defaultRunDevice = "Pippa"

// version is set by ldflags at build time.
var version = "dev"

const usage = `Usage: spyder [command]

Commands:
  (none)    MCP stdio proxy — auto-starts daemon if needed
  serve     Start the persistent background daemon
  run       Run a command, then foreground KeepAwake on the device
  version   Print version and exit

Run:
  spyder run [--device <alias>] -- <command> [args...]

  Executes <command> with its args, waits for it to exit, and then
  foregrounds KeepAwake on the device (default: Pippa). Exits with the
  command's exit code. Useful for wrapping xcodebuild test to restore
  the keep-awake state after the test runner finishes.
`

func main() {
	if len(os.Args) < 2 {
		runMCP()
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "serve":
		runServe()
	case "run":
		runCmd(os.Args[2:])
	case "version", "--version", "-version":
		fmt.Printf("spyder %s\n", version)
	case "help", "--help", "-help":
		fmt.Print(usage)
	default:
		if strings.HasPrefix(cmd, "-") {
			runMCP()
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
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
// unknown names). Package-level so `spyder run` bypasses the daemon
// entirely — the test wrapper needs no running mcpbridge server.
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
			return fmt.Errorf("KeepAwake on Android is not yet implemented (🎯T6)")
		}
	}
	adapter := device.NewIOSAdapter()
	return adapter.LaunchKeepAwake(id)
}

func runServe() {
	if err := daemon.Start(paths.SocketPath()); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

func runMCP() {
	ensureDaemon()

	if err := mcpbridge.RunProxy(context.Background(), mcpbridge.ProxyConfig{
		SocketPath: paths.SocketPath(),
		ServerName: "spyder",
		Version:    version,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "spyder: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: the daemon may have stopped — it will auto-start on next invocation\n")
		os.Exit(1)
	}
}

// ensureDaemon re-execs `spyder serve` in the background if no daemon is
// listening. Uses Setsid so the child survives the parent's exit.
func ensureDaemon() {
	sock := paths.SocketPath()
	if daemon.Running(sock) {
		return
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot find own executable: %v\n", err)
		os.Exit(1)
	}
	cmd := exec.Command(exe, "serve")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}
	_ = cmd.Process.Release()

	for range 30 {
		time.Sleep(100 * time.Millisecond)
		if daemon.Running(sock) {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "daemon did not start within 3 seconds\n")
	os.Exit(1)
}
