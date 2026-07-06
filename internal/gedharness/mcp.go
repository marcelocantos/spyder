// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// DefaultLogCount is how many recent ged log entries LogsViaMCP requests
// (ged caps at 200).
const DefaultLogCount = 200

// LogsViaMCP fetches ged's recent log history by calling its `logs` MCP tool
// over the /mcp streamable-HTTP endpoint. ged serves its whole control plane
// (info, tweak_list/get/set/reset, logs) as MCP tools via mark3labs/mcp-go —
// the canonical interface spyder's app-channel must replicate — and `logs`
// has no plain-HTTP route, so this is how the corpus captures it. baseURL is
// ged's base (e.g. http://localhost:42069); the tool endpoint is baseURL/mcp.
//
// mark3labs/mcp-go is already a spyder dependency (the daemon's MCP server),
// so this adds no new module.
func LogsViaMCP(ctx context.Context, baseURL string, count int) (string, error) {
	if count <= 0 {
		count = DefaultLogCount
	}
	c, err := client.NewStreamableHttpClient(strings.TrimRight(baseURL, "/") + "/mcp")
	if err != nil {
		return "", fmt.Errorf("gedharness: mcp client: %w", err)
	}
	defer c.Close()
	if err := c.Start(ctx); err != nil {
		return "", fmt.Errorf("gedharness: mcp start: %w", err)
	}

	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "gedharness", Version: "0.1.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return "", fmt.Errorf("gedharness: mcp initialize: %w", err)
	}

	var callReq mcp.CallToolRequest
	callReq.Params.Name = "logs"
	callReq.Params.Arguments = map[string]any{"count": count}
	res, err := c.CallTool(ctx, callReq)
	if err != nil {
		return "", fmt.Errorf("gedharness: mcp call logs: %w", err)
	}

	var sb strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), nil
}
