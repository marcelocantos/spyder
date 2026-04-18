// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package daemon runs spyder as an HTTP-based MCP server. Clients (e.g.
// Claude Code) connect via the streamable HTTP transport at /mcp.
package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
)

// Start creates the MCP server, registers all spyder tools, wraps it in
// a streamable-HTTP transport, and blocks serving on addr (e.g. ":3030").
func Start(addr, version string) error {
	srv := server.NewMCPServer(
		"spyder",
		version,
		server.WithToolCapabilities(true),
	)

	handler := spydermcp.NewHandler()

	for _, tool := range spydermcp.Definitions() {
		toolName := tool.Name
		srv.AddTool(tool, func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			text, isErr, err := handler.Call(toolName, req.GetArguments())
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result := mcp.NewToolResultText(text)
			if isErr {
				result.IsError = true
			}
			return result, nil
		})
	}

	slog.Info("spyder mcp server listening", "addr", addr, "endpoint", "/mcp")

	http := server.NewStreamableHTTPServer(srv)
	if err := http.Start(addr); err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}
