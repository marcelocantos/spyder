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
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/marcelocantos/spyder/internal/autoawake"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/tunneld"
)

// Config configures a spyder server instance.
type Config struct {
	Addr             string // HTTP listen address (e.g. ":3030"). ":0" picks a free port.
	Version          string // emitted in serverInfo
	TunneldAddr      string // tunneld probe target (host:port; empty → DefaultAddr)
	DisableAutoAwake bool   // tests set this to avoid the supervisor poking live tunneld
}

// Start creates the MCP server, registers all spyder tools, wraps it in
// a streamable-HTTP transport, probes tunneld for observability, and
// blocks serving on cfg.Addr until a SIGINT/SIGTERM arrives.
func Start(cfg Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return Run(ctx, cfg)
}

// Run is Start's cancellable sibling: it returns when ctx is cancelled
// (or the underlying HTTP server errors). Exposed for tests and for
// embedders that want to own signal handling.
func Run(ctx context.Context, cfg Config) error {
	httpSrv, tunClient := Build(cfg)

	if !cfg.DisableAutoAwake {
		go autoawake.New(tunClient).Run(ctx)
	}

	slog.Info("spyder mcp server listening", "addr", cfg.Addr, "endpoint", "/mcp")

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Start(cfg.Addr) }()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}
}

// Build wires up the MCP server and the streamable-HTTP handler
// without starting a listener or the autoawake supervisor. Tests
// (and embedders) can compose this with their own transport.
//
// The returned StreamableHTTPServer is a drop-in http.Handler via
// its ServeHTTP method, suitable for httptest.NewServer.
func Build(cfg Config) (*server.StreamableHTTPServer, *tunneld.Client) {
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

	return server.NewStreamableHTTPServer(srv), tunClient
}
