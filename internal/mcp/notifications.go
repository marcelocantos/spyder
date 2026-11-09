// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"time"

	"github.com/marcelocantos/spyder/internal/notify"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// SetNotifications is called before serving requests.
func (h *Handler) SetNotifications(m *notify.Manager) { h.notifications = m }

func (h *Handler) RunNotifications(ctx context.Context) {
	if h.notifications != nil {
		h.notifications.Run(ctx)
	}
}

func (h *Handler) handleNotifyWhenConnected(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.notifications == nil {
		return toolErr("notifications unavailable; check daemon startup log")
	}
	ref, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	webhook, err := requireString(args, "webhook_url")
	if err != nil {
		return nil, err
	}
	platform, id := optString(args, "platform"), ref
	if entry, ok := h.inventory.Lookup(ref); ok {
		if platform != "" && platform != entry.Platform {
			return toolErr("platform does not match inventory")
		}
		platform = entry.Platform
		switch platform {
		case "ios":
			id = entry.IOSUUID
		case "android":
			id = entry.AndroidSerial
		}
	} else if platform == "" {
		return toolErr("unknown device; use an inventory alias or pass platform with a hardware UDID / Android serial")
	}
	ttl := notify.DefaultTTL
	if v := optString(args, "ttl"); v != "" {
		ttl, err = time.ParseDuration(v)
		if err != nil {
			return toolErr("ttl must be a duration such as 24h")
		}
		if ttl < time.Second || ttl > notify.MaxTTL {
			return toolErr("ttl must be between 1s and 168h")
		}
	}
	if platform == "ios" && len(id) == 36 {
		return toolErr("iOS connection notifications require the hardware UDID, not a CoreDevice UUID")
	}
	s, err := h.notifications.Add(notify.Request{Device: ref, DeviceID: id, Platform: platform, WebhookURL: webhook, TTL: ttl})
	if err != nil {
		return toolErr("notify_when_connected: %v", err)
	}
	return toolJSON(s)
}

func (h *Handler) handleNotifications(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.notifications == nil {
		return toolErr("notifications unavailable; check daemon startup log")
	}
	return toolJSON(h.notifications.List())
}

func (h *Handler) handleNotificationCancel(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.notifications == nil {
		return toolErr("notifications unavailable; check daemon startup log")
	}
	id, err := requireString(args, "id")
	if err != nil {
		return nil, err
	}
	s, err := h.notifications.Cancel(id)
	if err != nil {
		return toolErr("notification_cancel: %v", err)
	}
	return toolJSON(s)
}

func notificationDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("notify_when_connected", mcpgo.WithDescription("Durable one-shot callback when a device connects, including if already connected. First create a T3 Code webhook task bound to this thread, with replayKey={{body.event_id}} and no coalesce delay; pass its webhookUrl here. Returns immediately; survives Spyder restart; retries callbacks until ttl expires. Connected means transport present (Android authorized), not unlocked, tunnel/automation ready or reserved. URLs are private and never returned."),
			mcpgo.WithString("device", mcpgo.Required(), mcpgo.Description("Inventory alias, or hardware UDID / Android serial with platform")),
			mcpgo.WithString("webhook_url", mcpgo.Required(), mcpgo.Description("T3 schedule_task webhookUrl; HTTPS, or loopback HTTP")),
			mcpgo.WithString("platform", mcpgo.Description("ios or android; required only for raw identifiers absent from inventory")),
			mcpgo.WithString("ttl", mcpgo.Description("Wait/delivery lifetime, default 24h, maximum 168h"))),
		mcpgo.NewTool("notifications", mcpgo.WithDescription("List durable connection notifications and delivery/error state, without webhook credentials.")),
		mcpgo.NewTool("notification_cancel", mcpgo.WithDescription("Cancel a waiting notification or callback retry. A callback already accepted cannot be recalled. Delete the T3 webhook task separately when no longer needed."), mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Notification id"))),
	}
}
