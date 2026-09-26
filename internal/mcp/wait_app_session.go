// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// wait_app_session polls app_channel_list until a bundle has a live session.

package mcp

import (
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// handleWaitAppSession blocks until app_channel_list reports a session for
// (device, bundle_id) or timeout_ms elapses.
func (h *Handler) handleWaitAppSession(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.appChannel == nil {
		return toolErr("app channel not configured")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	bundle, err := requireString(args, "bundle_id")
	if err != nil {
		return nil, err
	}
	timeout := 120_000
	if ms, ok := args["timeout_ms"].(float64); ok && ms > 0 {
		timeout = int(ms)
	}
	if timeout > 600_000 {
		timeout = 600_000
	}
	poll := 1000
	if ms, ok := args["poll_ms"].(float64); ok && ms > 0 {
		poll = int(ms)
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for {
		sid, port, listenerID, ok := h.findLiveSession(dev, bundle)
		if ok {
			return toolJSON(map[string]any{
				"device":      dev,
				"bundle_id":   bundle,
				"session_id":  sid,
				"listener_id": listenerID,
				"port":        port,
			})
		}
		if time.Now().After(deadline) {
			return toolErr("wait_app_session: no live session for %s on %s within %dms — deploy/launch the app first", bundle, dev, timeout)
		}
		time.Sleep(time.Duration(poll) * time.Millisecond)
	}
}

func (h *Handler) findLiveSession(dev, bundle string) (sessionID string, port int, listenerID string, ok bool) {
	resolved := dev
	if _, _, id, err := h.resolveAdapter(dev); err == nil && id != "" {
		resolved = id
	}
	listeners := h.appChannel.KeyedListeners()
	for _, l := range listeners {
		if l.Key.BundleID != bundle {
			continue
		}
		if l.Key.DeviceID != dev && l.Key.DeviceID != resolved {
			continue
		}
		sessions := l.Sessions()
		if len(sessions) == 0 {
			continue
		}
		return sessions[0].ID, l.Port, l.ID, true
	}
	return "", 0, "", false
}
