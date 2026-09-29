// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
)

// 🎯T154 — read and answer OS system alerts (permission prompts) on
// physical devices, for scripts and Verify steps.

// systemAlertPoll is how often system_alert_tap re-reads the screen while
// waiting for its alert to appear.
const systemAlertPoll = time.Second

// systemAlertReader resolves dev to an adapter that reads system alerts.
// The handler lock covers only the resolution: alert sessions take seconds
// and must not hold up other device verbs.
func (h *Handler) systemAlertReader(dev, owner string, mutate bool) (device.SystemAlertReader, string, string, *mcpgo.CallToolResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var adapter device.Adapter
	var platform, id string
	if mutate {
		var res *mcpgo.CallToolResult
		if adapter, platform, id, res = h.requireOSControlCap(dev, owner); res != nil {
			return nil, "", "", res
		}
	} else {
		var err error
		if adapter, platform, id, err = h.resolveAdapter(dev); err != nil {
			res, _ := toolErr("%v", err)
			return nil, "", "", res
		}
	}
	reader, ok := adapter.(device.SystemAlertReader)
	if !ok {
		res, _ := toolErr("system alerts are supported on iOS devices only (%s is %s)", dev, platform)
		return nil, "", "", res
	}
	return reader, platform, id, nil
}

func (h *Handler) handleSystemAlert(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	reader, _, id, res := h.systemAlertReader(dev, "", false)
	if res != nil {
		return res, nil
	}
	alert, err := reader.SystemAlert(id)
	if err != nil {
		return toolErr("system_alert on %s: %v", dev, err)
	}
	return toolJSON(map[string]any{"device": dev, "alert": alert})
}

func (h *Handler) handleSystemAlertTap(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	button, err := requireString(args, "button")
	if err != nil {
		return nil, err
	}
	wait := time.Duration(0)
	if ms, ok := args["wait_ms"].(float64); ok && ms > 0 {
		wait = time.Duration(ms) * time.Millisecond
	}
	reader, _, id, res := h.systemAlertReader(dev, optString(args, "owner"), true)
	if res != nil {
		return res, nil
	}
	// A freshly launched app raises its alert a few seconds later: wait for
	// an alert offering this button before tapping.
	deadline := time.Now().Add(wait)
	for {
		alert, err := reader.SystemAlert(id)
		if err != nil {
			return toolErr("system_alert_tap on %s: %v", dev, err)
		}
		if alert.Showing && device.AlertHasButton(alert, button) || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(systemAlertPoll)
	}
	before, after, err := reader.TapSystemAlertButton(id, button)
	if err != nil {
		if errors.Is(err, device.ErrNoSystemAlert) && wait > 0 {
			return toolErr("system_alert_tap on %s: no system alert appeared within %s", dev, wait)
		}
		return toolErr("system_alert_tap on %s: %v", dev, err)
	}
	return toolJSON(map[string]any{"device": dev, "tapped": button, "before": before, "after": after})
}

func systemAlertDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("system_alert",
			mcpgo.WithDescription("Report whether an OS system alert (a permission prompt such as Local Network, notifications or App Tracking Transparency) is showing on an iOS device, with its title, message and button labels. Reads through the accessibility inspector service; no test runner needed. Read-only. (🎯T154)"),
			mcpgo.WithString("device", mcpgo.Required(), mcpgo.Description("Device alias or UDID")),
		),
		mcpgo.NewTool("system_alert_tap",
			mcpgo.WithDescription("Tap a named button on the OS system alert showing on an iOS device, e.g. button=\"Allow\". Matching ignores case and apostrophe style. Fails clearly when no alert shows or it has no such button. Taps through Spyder's alert runner (install once with `make alert-runner`); iOS must allow UI automation on the device. Reservation-gated. (🎯T154)"),
			mcpgo.WithString("device", mcpgo.Required(), mcpgo.Description("Device alias or UDID")),
			mcpgo.WithString("button", mcpgo.Required(), mcpgo.Description("Button label, e.g. Allow or Don't Allow")),
			mcpgo.WithNumber("wait_ms", mcpgo.Description("Wait up to this long for an alert offering the button to appear (default 0: it must already show)")),
			mcpgo.WithString("owner", mcpgo.Description("Reservation owner")),
		),
	}
}
