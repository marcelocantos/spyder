// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package mcp implements the spyder MCP tool handler.
// Handler methods return *mcpgo.CallToolResult directly so tools can
// emit image/binary content (e.g. screenshot PNGs) without the daemon
// wrapper needing tool-specific wiring.
package mcp

import (
	"fmt"
	"sync"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/network"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// appliedNetwork tracks a network profile applied to a device by a
// specific owner, so it can be cleared automatically on reservation release.
type appliedNetwork struct {
	profile network.NetworkProfile
	owner   string
}

// Handler implements the spyder tool handler.
type Handler struct {
	mu           sync.Mutex
	inventory    *inventory.Store
	ios          device.Adapter
	android      device.Adapter
	tunneld      TunneldGate
	reservations *reservations.Store

	// networkByDevice maps a normalised device reference to the most
	// recently applied network profile for that device. Cleared when
	// the owning reservation is released.
	networkByDevice map[string]appliedNetwork
}

// TunneldGate is satisfied by *tunneld.Client. The small interface lets
// tests inject a fake without a circular package dependency.
type TunneldGate interface {
	Require() error
	Addr() string
}

// HandlerOption configures a Handler at construction.
type HandlerOption func(*Handler)

// WithReservations injects a reservation store so the handler can
// enforce strict holds on mutating tools. If omitted, all mutating
// tools run without any reservation checks (useful for tests).
func WithReservations(s *reservations.Store) HandlerOption {
	return func(h *Handler) { h.reservations = s }
}

// WithInventory injects a shared inventory store. Useful when the
// same inventory view is needed elsewhere (e.g. reservation
// normalization). Defaults to inventory.New().
func WithInventory(inv *inventory.Store) HandlerOption {
	return func(h *Handler) { h.inventory = inv }
}

// NewHandler creates a new spyder tool handler. tun may be nil for
// handler instances that never call DVT-dependent tools; tools that
// need it will return a clear error when tun is missing.
func NewHandler(tun TunneldGate, opts ...HandlerOption) *Handler {
	h := &Handler{
		inventory:       inventory.New(),
		ios:             device.NewIOSAdapter(),
		android:         device.NewAndroidAdapter(),
		tunneld:         tun,
		networkByDevice: map[string]appliedNetwork{},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Dispatch routes a tool call by name to its handler.
func (h *Handler) Dispatch(name string, args map[string]any) (*mcpgo.CallToolResult, error) {
	switch name {
	case "devices":
		return h.handleDevices(args)
	case "resolve":
		return h.handleResolve(args)
	case "keepawake":
		return h.handleKeepAwake(args)
	case "device_state":
		return h.handleDeviceState(args)
	case "screenshot":
		return h.handleScreenshot(args)
	case "list_apps":
		return h.handleListApps(args)
	case "launch_app":
		return h.handleLaunchApp(args)
	case "terminate_app":
		return h.handleTerminateApp(args)
	case "reserve":
		return h.handleReserve(args)
	case "release":
		return h.handleRelease(args)
	case "renew":
		return h.handleRenew(args)
	case "reservations":
		return h.handleReservations(args)
	case "network":
		return h.handleNetwork(args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
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
			mcpgo.WithDescription("Foreground the KeepAwake companion app on a device so it holds the screen awake while plugged in. Typically called by test-run wrappers after tests finish. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("device_state",
			mcpgo.WithDescription("Report current device state: battery level, thermal state, charging status, foreground app. Read-only; not subject to reservations."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
		),

		mcpgo.NewTool("screenshot",
			mcpgo.WithDescription("Capture a PNG screenshot of the device. Returns the image inline for the agent to inspect. iOS uses pymobiledevice3 developer dvt (requires tunneld); Android uses adb shell screencap. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("list_apps",
			mcpgo.WithDescription("List installed third-party apps on the device with bundle id, and (iOS only) display name and version. Read-only; not subject to reservations."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
		),

		mcpgo.NewTool("launch_app",
			mcpgo.WithDescription("Foreground an app by bundle id. iOS uses pymobiledevice3 dvt launch (requires tunneld); Android uses adb monkey with the LAUNCHER intent. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Required(),
				mcpgo.Description("App bundle identifier (e.g. com.example.app)"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("terminate_app",
			mcpgo.WithDescription("Terminate a running app by bundle id. iOS resolves the PID via dvt then kills (requires tunneld); Android uses adb am force-stop. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Required(),
				mcpgo.Description("App bundle identifier (e.g. com.example.app)"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("reserve",
			mcpgo.WithDescription("Acquire an exclusive reservation on a device so parallel sessions won't interrupt mutating operations (keepawake, screenshot, launch/terminate). Default TTL is 3600s, max 86400s. Same-owner re-acquires renew in place."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Required(),
				mcpgo.Description("Free-form owner identity; convention is the project basename (e.g. 'tiltbuggy')"),
			),
			mcpgo.WithNumber("ttl_seconds",
				mcpgo.Description("Reservation lifetime in seconds (default 3600, max 86400)"),
			),
			mcpgo.WithString("note",
				mcpgo.Description("Human-readable note surfaced in conflict errors (e.g. 'UI regression run')"),
			),
		),

		mcpgo.NewTool("release",
			mcpgo.WithDescription("Release a reservation held by the given owner. Freeing a device you don't own returns a Conflict; freeing an unreserved device is a no-op."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Required(),
				mcpgo.Description("Owner identity under which the reservation was taken"),
			),
		),

		mcpgo.NewTool("renew",
			mcpgo.WithDescription("Extend the TTL on an existing reservation. Only the owner can renew. Useful for long-running workflows that outlive the default TTL."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Required(),
				mcpgo.Description("Owner identity under which the reservation was taken"),
			),
			mcpgo.WithNumber("ttl_seconds",
				mcpgo.Description("New reservation lifetime in seconds from now (default 3600, max 86400)"),
			),
		),

		mcpgo.NewTool("reservations",
			mcpgo.WithDescription("List all active reservations across all devices. Read-only."),
		),

		mcpgo.NewTool("network",
			mcpgo.WithDescription(
				"Apply or clear network condition shaping on a device. "+
					"Supported on Android emulators via the adb console. "+
					"iOS (simulator and physical) and physical Android devices are not supported — "+
					"a clear error is returned for those targets.\n\n"+
					"Named profiles: wifi (full-speed), 4g, 3g, edge, gsm, offline.\n"+
					"Dynamic profiles: lossy-<pct> (0–100% packet loss), delay-<ms> (extra one-way latency).\n\n"+
					"NOTE — packet loss (lossy-<pct>) is not implemented by the adb console protocol. "+
					"The profile is partially applied (speed/delay) and an error is returned describing the gap.\n\n"+
					"Applied profiles are cleared automatically when the reservation for the device is released. "+
					"If the daemon exits abnormally before a release, the emulator retains the last applied profile "+
					"until the next ApplyNetwork or ClearNetwork call, or the emulator is restarted.",
			),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Required(),
				mcpgo.Description("Reservation owner to authenticate as"),
			),
			mcpgo.WithString("profile",
				mcpgo.Description("Network profile to apply: wifi, 4g, 3g, edge, gsm, offline, lossy-<pct>, delay-<ms>. Mutually exclusive with clear."),
			),
			mcpgo.WithBoolean("clear",
				mcpgo.Description("If true, clear the applied profile and restore full-speed connectivity. Mutually exclusive with profile."),
			),
		),
	}
}
