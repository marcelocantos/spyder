// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/logcapture"
)

// handleLogCaptureStart begins a server-managed capture session against
// a device and returns the session_id. Reads are subsequently issued via
// log_capture_get (peek, keep capturing) or log_capture_stop (drain and
// tear down). (🎯T60.)
func (h *Handler) handleLogCaptureStart(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.logCapture == nil {
		return toolErr("log capture not configured")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	processArg := optString(args, "process")
	bundleArg := optString(args, "bundle_id")
	if processArg != "" && bundleArg != "" {
		return toolErr("process and bundle_id are mutually exclusive — pass one or the other")
	}

	filter := device.LogFilter{
		Process:   processArg,
		Subsystem: optString(args, "subsystem"),
		Tag:       optString(args, "tag"),
		Regex:     optString(args, "regex"),
	}

	h.mu.Lock()
	adapter, _, id, adapterErr := h.resolveAdapter(dev)
	h.mu.Unlock()
	if adapterErr != nil {
		return toolErr("%v", adapterErr)
	}

	if bundleArg != "" {
		exe, installed, err := adapter.ResolveExecutable(id, bundleArg)
		if err != nil {
			return toolErr("bundle_id %s on %s: %v", bundleArg, dev, err)
		}
		if !installed {
			return toolErr("bundle_id %s not installed on %s", bundleArg, dev)
		}
		filter.Process = exe
	}

	var ttl time.Duration
	if ttlSec, ok := args["ttl_sec"].(float64); ok && ttlSec > 0 {
		ttl = time.Duration(ttlSec) * time.Second
	}

	maxBytes, _ := args["max_bytes"].(float64)
	maxLines, _ := args["max_lines"].(float64)

	sess, err := h.logCapture.Start(context.Background(), adapter, logcapture.StartParams{
		Device:   dev,
		DeviceID: id,
		Filter:   filter,
		Owner:    optString(args, "owner"),
		TTL:      ttl,
		MaxBytes: int(maxBytes),
		MaxLines: int(maxLines),
	})
	if err != nil {
		return toolErr("log_capture_start: %v", err)
	}
	return toolJSON(struct {
		SessionID string    `json:"session_id"`
		Device    string    `json:"device"`
		Owner     string    `json:"owner,omitempty"`
		StartedAt time.Time `json:"started_at"`
		ExpiresAt time.Time `json:"expires_at"`
	}{
		SessionID: sess.ID,
		Device:    dev,
		Owner:     sess.Owner,
		StartedAt: sess.StartedAt,
		ExpiresAt: sess.StartedAt.Add(sess.TTL),
	})
}

// handleLogCaptureGet returns the lines accumulated since session start
// (or the last Get) without stopping capture. (🎯T60.)
func (h *Handler) handleLogCaptureGet(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.logCapture == nil {
		return toolErr("log capture not configured")
	}
	id, err := requireString(args, "session_id")
	if err != nil {
		return nil, err
	}
	res, err := h.logCapture.Get(id)
	if err != nil {
		return toolErr("log_capture_get: %v", err)
	}
	return toolJSON(res)
}

// handleLogCaptureStop drains the session's buffer and tears it down.
// Idempotent failure: calling Stop a second time on the same id returns
// an error. (🎯T60.)
func (h *Handler) handleLogCaptureStop(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.logCapture == nil {
		return toolErr("log capture not configured")
	}
	id, err := requireString(args, "session_id")
	if err != nil {
		return nil, err
	}
	res, err := h.logCapture.Stop(id)
	if err != nil {
		return toolErr("log_capture_stop: %v", err)
	}
	return toolJSON(res)
}

// handleLogCaptureList returns metadata for every live capture session.
// Read-only. (🎯T60.)
func (h *Handler) handleLogCaptureList(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.logCapture == nil {
		return toolErr("log capture not configured")
	}
	return toolJSON(h.logCapture.List())
}

// logCaptureDefinitions returns the MCP tool definitions for the
// managed capture session API. Exposed via Definitions().
func logCaptureDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("log_capture_start",
			mcpgo.WithDescription(fmt.Sprintf(
				"Begin a server-managed log-capture session against a device. Returns a session_id you can later pass to log_capture_get (peek without stopping) or log_capture_stop (drain and tear down). Sessions auto-expire after ttl_sec of no Get/Stop activity (default %ds, max %ds). Buffer is bounded (default %d MB / %d lines, FIFO eviction); dropped_lines in the get/stop response is non-zero when the buffer was full when a line arrived. Prefer this over the REST SSE --follow stream when an agent needs to capture across multiple agent turns — the agent doesn't have to hold a streaming connection open or manage a background shell.",
				int(logcapture.DefaultTTL.Seconds()),
				int(logcapture.MaxTTL.Seconds()),
				logcapture.DefaultMaxBytes/(1024*1024),
				logcapture.DefaultMaxLines)),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Description("iOS bundle id filter; mutually exclusive with process. Resolved server-side to CFBundleExecutable."),
			),
			mcpgo.WithString("process",
				mcpgo.Description("Process / executable name filter (CFBundleExecutable on iOS; package name on Android). Mutually exclusive with bundle_id."),
			),
			mcpgo.WithString("subsystem",
				mcpgo.Description("Subsystem substring filter (iOS only, server-side)"),
			),
			mcpgo.WithString("tag",
				mcpgo.Description("Tag substring filter (Android only)"),
			),
			mcpgo.WithString("regex",
				mcpgo.Description("Regex applied to the message column (client-side filter)"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Free-form owner string for visibility in log_capture_list; convention is filepath.Base(cwd)"),
			),
			mcpgo.WithNumber("ttl_sec",
				mcpgo.Description(fmt.Sprintf("Auto-expiry budget in seconds (default %d, max %d)", int(logcapture.DefaultTTL.Seconds()), int(logcapture.MaxTTL.Seconds()))),
			),
			mcpgo.WithNumber("max_bytes",
				mcpgo.Description(fmt.Sprintf("Bound the session buffer at this many bytes (default %d, ~50 MB). When exceeded, oldest lines are evicted FIFO and counted in dropped_lines.", logcapture.DefaultMaxBytes)),
			),
			mcpgo.WithNumber("max_lines",
				mcpgo.Description(fmt.Sprintf("Bound the session buffer at this many lines (default %d). When exceeded, oldest lines are evicted FIFO and counted in dropped_lines.", logcapture.DefaultMaxLines)),
			),
		),

		mcpgo.NewTool("log_capture_get",
			mcpgo.WithDescription("Return the lines buffered in a capture session since it started (or since the last log_capture_get). Capture continues — call log_capture_stop to tear down. dropped_lines is the count of eviction events since the last drain; non-zero means the buffer hit its limit between calls."),
			mcpgo.WithString("session_id",
				mcpgo.Required(),
				mcpgo.Description("session_id returned by log_capture_start"),
			),
		),

		mcpgo.NewTool("log_capture_stop",
			mcpgo.WithDescription("Drain and tear down a capture session, returning the lines remaining in its buffer plus any cumulative dropped_lines. The session is gone after this call; subsequent log_capture_get / log_capture_stop on the same session_id return an error."),
			mcpgo.WithString("session_id",
				mcpgo.Required(),
				mcpgo.Description("session_id returned by log_capture_start"),
			),
		),

		mcpgo.NewTool("log_capture_list",
			mcpgo.WithDescription("Return metadata for every live capture session: session_id, device, owner, started_at, expires_at, buffer_lines, buffer_bytes, dropped_lines, filter. Read-only."),
		),
	}
}
