// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package daemon runs spyder as an HTTP-based MCP server. Clients (e.g.
// Claude Code) connect via the streamable HTTP transport at /mcp.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/marcelocantos/spyder/internal/autoawake"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/tunneld"
)

// Config configures a spyder server instance.
type Config struct {
	Addr        string // HTTP listen address (e.g. ":3030")
	Version     string // emitted in serverInfo
	TunneldAddr string // tunneld probe target (host:port; empty → DefaultAddr)
}

// Start creates the MCP server, registers all spyder tools, wraps it in
// a streamable-HTTP transport, probes tunneld for observability, and
// blocks serving on cfg.Addr.
func Start(cfg Config) error {
	tunneldAddr := cfg.TunneldAddr
	if tunneldAddr == "" {
		tunneldAddr = tunneld.DefaultAddr
	}
	tunClient := tunneld.New(tunneldAddr)
	if udids, err := tunClient.Probe(); err != nil {
		slog.Warn("tunneld unavailable — iOS DVT tools will fail",
			"addr", tunneldAddr, "error", err)
	} else {
		slog.Info("tunneld reachable", "addr", tunneldAddr, "paired_devices", len(udids))
	}

	srv := server.NewMCPServer(
		"spyder",
		cfg.Version,
		server.WithToolCapabilities(true),
	)

	handler := spydermcp.NewHandler(tunClient)

	for _, tool := range spydermcp.Definitions() {
		toolName := tool.Name
		srv.AddTool(tool, func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handler.Dispatch(toolName, req.GetArguments())
		})
	}

	// Auto-awake supervisor: watches tunneld for iOS devices and
	// ensures KeepAwake is running on each. Exits when ctx is done.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go autoawake.New(tunClient).Run(ctx)

	slog.Info("spyder mcp server listening", "addr", cfg.Addr, "endpoint", "/mcp")

	http := server.NewStreamableHTTPServer(srv)
	errCh := make(chan error, 1)
	go func() { errCh <- http.Start(cfg.Addr) }()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}
}
