// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package daemon starts the spyder daemon: a persistent background process
// that owns the device inventory, tracks session state, and serves MCP tool
// calls over a Unix domain socket.
package daemon

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/marcelocantos/mcpbridge"

	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
)

// Running reports whether a daemon is accepting connections on socketPath.
// A stale socket file with no listener is not "running".
func Running(socketPath string) bool {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Start starts the daemon, listening on socketPath. Blocks until SIGINT or
// SIGTERM is received. Refuses to start if another daemon is already
// listening on the socket (mcpbridge would otherwise unlink and silently
// take over, leaving two processes claiming to own the socket).
func Start(socketPath string) error {
	if Running(socketPath) {
		return fmt.Errorf("daemon already listening on %s", socketPath)
	}

	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return fmt.Errorf("creating socket directory: %w", err)
	}

	handler := spydermcp.NewHandler()

	srv, err := mcpbridge.NewServer(mcpbridge.DaemonConfig{
		SocketPath: socketPath,
		Tools:      spydermcp.Definitions(),
		Handler:    handler,
	})
	if err != nil {
		return fmt.Errorf("creating server: %w", err)
	}

	if err := os.Chmod(socketPath, 0o600); err != nil {
		srv.Close()
		return fmt.Errorf("tightening socket permissions: %w", err)
	}

	slog.Info("spyder daemon listening", "socket", socketPath)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutting down")
		srv.Close()
	}()

	return srv.Serve()
}
