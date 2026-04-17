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
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/marcelocantos/mcpbridge"

	"github.com/marcelocantos/spyder/internal/daemon"
	"github.com/marcelocantos/spyder/internal/paths"
)

// version is set by ldflags at build time.
var version = "dev"

const usage = `Usage: spyder [command]

Commands:
  (none)    MCP stdio proxy — auto-starts daemon if needed
  serve     Start the persistent background daemon
  version   Print version and exit
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

func daemonRunning(socketPath string) bool {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ensureDaemon re-execs `spyder serve` in the background if no daemon is
// listening. Uses Setsid so the child survives the parent's exit.
func ensureDaemon() {
	sock := paths.SocketPath()
	if daemonRunning(sock) {
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
		if daemonRunning(sock) {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "daemon did not start within 3 seconds\n")
	os.Exit(1)
}
