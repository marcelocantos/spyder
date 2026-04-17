// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package daemon starts the spyder daemon: a persistent background process
// that owns the device inventory, tracks session state, and serves MCP tool
// calls over a Unix domain socket.
package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/marcelocantos/mcpbridge"

	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
)

// Start starts the daemon, listening on socketPath. Blocks until SIGINT or
// SIGTERM is received.
func Start(socketPath string) error {
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
