// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/rand"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/notify"
	"github.com/marcelocantos/spyder/internal/reservations"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// SetNotifications is called before serving requests.
func (h *Handler) SetNotifications(m *notify.Manager) {
	h.notifications = m
	if m == nil {
		return
	}
	if h.appChannel != nil {
		h.appChannel.SetOnSession(func(kind string, s *appchannel.Session) {
			key := s.Listener().Key
			h.publishNotification(notify.Event{Event: "app." + kind, DeviceID: key.DeviceID, Data: map[string]string{"bundle_id": key.BundleID, "session_id": s.ID, "owner": s.Owner}})
		})
	}
	if h.reservations != nil {
		h.reservations.SetOnChange(func(kind string, r reservations.Reservation) {
			h.publishNotification(notify.Event{Event: "reservation." + kind, Device: r.Device, Data: map[string]string{"owner": r.Owner}})
		})
	}
	if h.health != nil {
		h.health.Model().OnTransition(func(tr health.Transition) {
			if tr.From != tr.To {
				h.publishNotification(notify.Event{Event: "health.changed", OccurredAt: tr.At, Data: map[string]string{"kind": string(tr.ID.Kind), "name": tr.ID.Name, "layer": tr.ID.Layer, "from": string(tr.From), "state": string(tr.To)}})
			}
		})
	}
}

func (h *Handler) publishNotification(e notify.Event) {
	if h.notifications == nil {
		return
	}
	e = h.enrichNotificationEvent(e)
	if err := h.notifications.Publish(e); err != nil {
		slog.Error("notification event enqueue failed", "event", e.Event, "error", err)
	}
}

func (h *Handler) enrichNotificationEvent(e notify.Event) notify.Event {
	ref := e.DeviceID
	if ref == "" {
		ref = e.Device
	}
	if h.inventory != nil && ref != "" {
		if entry, ok := h.inventory.Lookup(ref); ok {
			e.Device = entry.Alias
			e.Platform = entry.Platform
			switch entry.Platform {
			case "ios":
				e.DeviceID = entry.IOSUUID
			case "android":
				e.DeviceID = entry.AndroidSerial
			case "desktop":
				e.DeviceID = entry.ExecutablePath
			}
		}
	}
	return e
}

func (h *Handler) RunNotifications(ctx context.Context) {
	if h.notifications != nil {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); h.notifications.RunDeviceEvents(ctx, h.publishNotification) }()
		if h.reservations != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tick := time.NewTicker(time.Second)
				defer tick.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
						h.reservations.List()
					}
				}
			}()
		}
		defer wg.Wait()
		h.notifications.Run(ctx)
	}
}

func (h *Handler) handleNotificationSubscribe(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.notifications == nil {
		return toolErr("notifications unavailable; check daemon startup log")
	}
	webhook, err := requireString(args, "webhook_url")
	if err != nil {
		return nil, err
	}
	var events []string
	switch v := args["events"].(type) {
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return toolErr("events must be strings")
			}
			events = append(events, s)
		}
	case []string:
		events = v
	default:
		return toolErr("events must be an array of names or family patterns; inspect notification_events")
	}
	match := map[string]string{}
	if raw, exists := args["match"]; exists {
		v, ok := raw.(map[string]any)
		if !ok {
			return toolErr("match must be an object of exact string values")
		}
		for key, value := range v {
			s, ok := value.(string)
			if !ok {
				return toolErr("match values must be strings")
			}
			match[key] = s
		}
	}
	// Normalize a device filter once, so aliases and hardware IDs match every source.
	if ref := match["device"]; ref != "" {
		if entry, ok := h.inventory.Lookup(ref); ok {
			match["device"] = entry.Alias
		}
	}
	ttl := notify.DefaultTTL
	if v := optString(args, "ttl"); v != "" {
		ttl, err = time.ParseDuration(v)
		if err != nil || ttl < time.Second || ttl > notify.MaxTTL {
			return toolErr("ttl must be between 1s and 168h")
		}
	}
	recurring, _ := args["recurring"].(bool)
	s, err := h.notifications.Subscribe(notify.SubscribeRequest{Events: events, Match: match, Recurring: recurring, WebhookURL: webhook, TTL: ttl})
	if err != nil {
		return toolErr("notification_subscribe: %v", err)
	}
	return toolJSON(s)
}

func (h *Handler) handleNotificationEvents(_ map[string]any) (*mcpgo.CallToolResult, error) {
	return toolJSON(notify.EventTypes())
}

func (h *Handler) handleEventPublish(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.notifications == nil {
		return toolErr("notifications unavailable")
	}
	name, err := requireString(args, "event")
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(name, "custom.") {
		return toolErr("event_publish requires a custom.* event name; lifecycle events come from their real sources")
	}
	data := map[string]string{}
	if raw, exists := args["data"]; exists {
		v, ok := raw.(map[string]any)
		if !ok {
			return toolErr("data must be an object of strings")
		}
		for key, value := range v {
			s, ok := value.(string)
			if !ok {
				return toolErr("event data values must be strings")
			}
			data[key] = s
		}
	}
	e := h.enrichNotificationEvent(notify.Event{EventID: rand.Text(), Event: name, Device: optString(args, "device"), Data: data})
	if err := h.notifications.Publish(e); err != nil {
		return toolErr("event_publish: %v", err)
	}
	return toolJSON(map[string]any{"published": true, "event": name, "event_id": e.EventID})
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
		mcpgo.NewTool("notification_events", mcpgo.WithDescription("Discover supported lifecycle event names and exact-match fields, including custom.* script events.")),
		mcpgo.NewTool("notification_subscribe", mcpgo.WithDescription("Durable event subscription delivered to a T3 webhook. events is an array of exact names or family patterns such as app.*; match is an object of exact string field values (all must match; missing fields do not match). One-shot by default; recurring=true delivers every matching event until cancelled or expired. Events after registration only, except device source startup sightings. Pending callbacks survive restart. Bound queue: 256 per subscription; overflow increments dropped and logs an error. See notification_events for names/fields."),
			mcpgo.WithArray("events", mcpgo.Required(), mcpgo.Description("Event names or family patterns")),
			mcpgo.WithObject("match", mcpgo.Description("Exact string field filters")),
			mcpgo.WithBoolean("recurring", mcpgo.Description("Keep subscription active after delivery (default false)")),
			mcpgo.WithString("webhook_url", mcpgo.Required(), mcpgo.Description("T3 webhookUrl")),
			mcpgo.WithString("ttl", mcpgo.Description("Lifetime, default 24h, maximum 168h"))),
		mcpgo.NewTool("event_publish", mcpgo.WithDescription("Publish a named custom.* event from a script/workflow. Built-in lifecycle names cannot be forged here. Data is delivered to subscribers; exclude secrets."), mcpgo.WithString("event", mcpgo.Required()), mcpgo.WithString("device"), mcpgo.WithObject("data")),
		mcpgo.NewTool("notify_when_connected", mcpgo.WithDescription("Durable one-shot callback when a device connects, including if already connected. First create a T3 Code webhook task bound to this thread, with replayKey={{body.event_id}} and no coalesce delay; pass its webhookUrl here. Returns immediately; survives Spyder restart; retries callbacks until ttl expires. Connected means transport present (Android authorized), not unlocked, tunnel/automation ready or reserved. URLs are private and never returned."),
			mcpgo.WithString("device", mcpgo.Required(), mcpgo.Description("Inventory alias, or hardware UDID / Android serial with platform")),
			mcpgo.WithString("webhook_url", mcpgo.Required(), mcpgo.Description("T3 schedule_task webhookUrl; HTTPS, or loopback HTTP")),
			mcpgo.WithString("platform", mcpgo.Description("ios or android; required only for raw identifiers absent from inventory")),
			mcpgo.WithString("ttl", mcpgo.Description("Wait/delivery lifetime, default 24h, maximum 168h"))),
		mcpgo.NewTool("notifications", mcpgo.WithDescription("List durable connection notifications and delivery/error state, without webhook credentials.")),
		mcpgo.NewTool("notification_cancel", mcpgo.WithDescription("Cancel a waiting notification or callback retry. A callback already accepted cannot be recalled. Delete the T3 webhook task separately when no longer needed."), mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Notification id"))),
	}
}
