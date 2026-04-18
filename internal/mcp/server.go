// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package mcp implements the spyder MCP tool handler using mcpbridge.
// It exposes the device inventory, keep-awake lifecycle, and device state
// reporting as MCP tools.
package mcp

import (
	"fmt"
	"sync"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
)

// Handler implements the spyder tool handler.
type Handler struct {
	mu        sync.Mutex
	inventory *inventory.Store
	ios       device.Adapter
	android   device.Adapter
	tunneld   TunneldGate
}

// TunneldGate is satisfied by *tunneld.Client. The small interface lets
// tests inject a fake without a circular package dependency.
type TunneldGate interface {
	Require() error
	Addr() string
}

// NewHandler creates a new spyder tool handler. tun may be nil for
// handler instances that never call DVT-dependent tools; tools that
// need it will return a clear error when tun is missing.
func NewHandler(tun TunneldGate) *Handler {
	return &Handler{
		inventory: inventory.New(),
		ios:       device.NewIOSAdapter(),
		android:   device.NewAndroidAdapter(),
		tunneld:   tun,
	}
}

// Call dispatches an MCP tool call by name.
func (h *Handler) Call(name string, args map[string]any) (string, bool, error) {
	switch name {
	case "devices":
		return h.handleDevices(args)
	case "resolve":
		return h.handleResolve(args)
	case "keepawake":
		return h.handleKeepAwake(args)
	case "device_state":
		return h.handleDeviceState(args)
	default:
		return "", false, fmt.Errorf("unknown tool: %s", name)
	}
}

// Definitions returns the MCP tool definitions for all spyder tools.
func Definitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("devices",
			mcpgo.WithDescription("List connected mobile devices across platforms, with alias, platform, model, and OS version."),
			mcpgo.WithString("platform",
				mcpgo.Description("Filter by platform: ios, android, or all (default)"),
			),
		),

		mcpgo.NewTool("resolve",
			mcpgo.WithDescription("Resolve a symbolic device name (e.g. 'Pippa') to its platform-specific UUIDs for use with xcodebuild, devicectl, pymobiledevice3, or adb."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Symbolic name or raw UUID from the device inventory"),
			),
		),

		mcpgo.NewTool("keepawake",
			mcpgo.WithDescription("Foreground the KeepAwake companion app on a device so it holds the screen awake while plugged in. Typically called by test-run wrappers after tests finish."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
		),

		mcpgo.NewTool("device_state",
			mcpgo.WithDescription("Report current device state: battery level, thermal state, charging status, foreground app."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
		),
	}
}
