// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/logcollect"
)

// handleLogCollectStart binds a fresh TCP port and starts a capture
// session for it. Apps configured with LOG_TARGET=<host>:<port> stream
// lines into the session's buffer; the agent reads them out with
// log_collect_get / log_collect_stop, mirroring the log_capture tools.
//
// (🎯T73 follow-up: env passthrough lets users inject LOG_TARGET at
// launch; this tool gives them somewhere for the app to dial.)
func (h *Handler) handleLogCollectStart(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.logCollect == nil {
		return toolErr("log collect not configured")
	}

	var ttl time.Duration
	if ttlSec, ok := args["ttl_sec"].(float64); ok && ttlSec > 0 {
		ttl = time.Duration(ttlSec) * time.Second
	}
	maxBytes, _ := args["max_bytes"].(float64)
	maxLines, _ := args["max_lines"].(float64)

	sess, err := h.logCollect.Start(logcollect.StartParams{
		Owner:    optString(args, "owner"),
		TTL:      ttl,
		MaxBytes: int(maxBytes),
		MaxLines: int(maxLines),
	})
	if err != nil {
		return toolErr("log_collect_start: %v", err)
	}

	hosts, _ := logcollect.LANHosts()

	return toolJSON(struct {
		SessionID string    `json:"session_id"`
		Port      int       `json:"port"`
		Hosts     []string  `json:"hosts"`
		Owner     string    `json:"owner,omitempty"`
		ExpiresAt time.Time `json:"expires_at"`
	}{
		SessionID: sess.ID,
		Port:      sess.Port,
		Hosts:     hosts,
		Owner:     sess.Owner,
		ExpiresAt: sess.StartedAt.Add(sess.TTL),
	})
}

// handleLogCollectGet returns the lines accumulated in a session
// without stopping it. Mirrors log_capture_get.
func (h *Handler) handleLogCollectGet(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.logCollect == nil {
		return toolErr("log collect not configured")
	}
	id, err := requireString(args, "session_id")
	if err != nil {
		return nil, err
	}
	r, err := h.logCollect.Get(id)
	if err != nil {
		return toolErr("log_collect_get: %v", err)
	}
	return toolJSON(r)
}

// handleLogCollectStop drains the session's buffer and closes the
// listener. Mirrors log_capture_stop.
func (h *Handler) handleLogCollectStop(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.logCollect == nil {
		return toolErr("log collect not configured")
	}
	id, err := requireString(args, "session_id")
	if err != nil {
		return nil, err
	}
	r, err := h.logCollect.Stop(id)
	if err != nil {
		return toolErr("log_collect_stop: %v", err)
	}
	return toolJSON(r)
}

// handleLogCollectList returns metadata for every live listener.
func (h *Handler) handleLogCollectList(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.logCollect == nil {
		return toolErr("log collect not configured")
	}
	return toolJSON(h.logCollect.List())
}

// logCollectDefinitions returns the MCP tool definitions for the
// inbound-TCP capture API.
func logCollectDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("log_collect_start",
			mcpgo.WithDescription(fmt.Sprintf(
				"Open a fresh TCP listener on a kernel-assigned port and start a capture session for it. Apps configured with LOG_TARGET=<host>:<port> (one of the returned host candidates) push newline-delimited log lines into the session's bounded buffer; the agent reads them out with log_collect_get / log_collect_stop. One port per session — connections on a port are unambiguously from the app that was launched against that LOG_TARGET, no in-band tagging required. Reconnects merge into the same session. Sessions auto-expire after ttl_sec of no Get/Stop activity (default %ds, max %ds). Buffer is bounded (default %d MB / %d lines, FIFO eviction); dropped_lines in get/stop responses is non-zero when the buffer was full when a line arrived. Each line longer than %d bytes is truncated to that cap on receipt.",
				int(logcollect.DefaultTTL.Seconds()),
				int(logcollect.MaxTTL.Seconds()),
				logcollect.DefaultMaxBytes/(1024*1024),
				logcollect.DefaultMaxLines,
				logcollect.MaxLineBytes)),
			mcpgo.WithString("owner",
				mcpgo.Description("Free-form owner string for visibility in log_collect_list; convention is filepath.Base(cwd)"),
			),
			mcpgo.WithNumber("ttl_sec",
				mcpgo.Description(fmt.Sprintf("Auto-expiry budget in seconds (default %d, max %d)", int(logcollect.DefaultTTL.Seconds()), int(logcollect.MaxTTL.Seconds()))),
			),
			mcpgo.WithNumber("max_bytes",
				mcpgo.Description(fmt.Sprintf("Bound the session buffer at this many bytes (default %d, ~50 MB).", logcollect.DefaultMaxBytes)),
			),
			mcpgo.WithNumber("max_lines",
				mcpgo.Description(fmt.Sprintf("Bound the session buffer at this many lines (default %d).", logcollect.DefaultMaxLines)),
			),
		),

		mcpgo.NewTool("log_collect_get",
			mcpgo.WithDescription("Return lines received since the session started (or since the last log_collect_get). Capture continues — call log_collect_stop to tear down."),
			mcpgo.WithString("session_id",
				mcpgo.Required(),
				mcpgo.Description("session_id returned by log_collect_start"),
			),
		),

		mcpgo.NewTool("log_collect_stop",
			mcpgo.WithDescription("Drain and tear down a collect session, closing its listener. Returns the lines remaining in its buffer plus any cumulative dropped_lines. The session is gone after this call."),
			mcpgo.WithString("session_id",
				mcpgo.Required(),
				mcpgo.Description("session_id returned by log_collect_start"),
			),
		),

		mcpgo.NewTool("log_collect_list",
			mcpgo.WithDescription("Return metadata for every live collect session: session_id, port, owner, started_at, expires_at, buffer_lines, buffer_bytes, dropped_lines, connections (lifetime total), active_conns (currently open), bytes_received (lifetime total). Read-only."),
		),
	}
}
