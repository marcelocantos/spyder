// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/runs"
)

func requireString(args map[string]any, key string) (string, error) {
	v, ok := args[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func optString(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// toolErr wraps a user-facing error as a non-nil CallToolResult with
// IsError=true so clients surface it in the tool-result channel rather
// than treating it as a transport fault.
func toolErr(format string, args ...any) (*mcpgo.CallToolResult, error) {
	return mcpgo.NewToolResultError(fmt.Sprintf(format, args...)), nil
}

// toolText returns a successful CallToolResult carrying text.
func toolText(text string) (*mcpgo.CallToolResult, error) {
	return mcpgo.NewToolResultText(text), nil
}

// toolJSON marshals v as pretty-printed JSON and returns it as text.
func toolJSON(v any) (*mcpgo.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return toolText(string(data))
}

func (h *Handler) handleDevices(args map[string]any) (*mcpgo.CallToolResult, error) {
	platform := optString(args, "platform")
	if platform == "" {
		platform = "all"
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var devices []device.Info
	var perAdapterErrors []string

	if platform == "ios" || platform == "all" {
		ds, err := h.ios.List()
		if err != nil {
			if platform == "ios" {
				return toolErr("ios: %v", err)
			}
			perAdapterErrors = append(perAdapterErrors, fmt.Sprintf("ios: %v", err))
		}
		devices = append(devices, ds...)
	}
	if platform == "android" || platform == "all" {
		ds, err := h.android.List()
		if err != nil {
			if platform == "android" {
				return toolErr("android: %v", err)
			}
			perAdapterErrors = append(perAdapterErrors, fmt.Sprintf("android: %v", err))
		}
		devices = append(devices, ds...)
	}

	for i := range devices {
		if alias := h.inventory.AliasFor(devices[i].UUID); alias != "" {
			devices[i].Alias = alias
		}
	}

	if platform == "all" && len(perAdapterErrors) > 0 {
		return toolJSON(struct {
			Devices []device.Info `json:"devices"`
			Errors  []string      `json:"errors,omitempty"`
		}{devices, perAdapterErrors})
	}
	return toolJSON(devices)
}

func (h *Handler) handleResolve(args map[string]any) (*mcpgo.CallToolResult, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	entry, ok := h.inventory.Lookup(name)
	if !ok {
		entry = inventory.ClassifyRaw(name)
	}
	return toolJSON(entry)
}

func (h *Handler) handleKeepAwake(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if err := adapter.LaunchKeepAwake(id); err != nil {
		return toolErr("launching KeepAwake on %s: %v", dev, err)
	}
	switch platform {
	case "android":
		return toolText(fmt.Sprintf("KeepAwake is a no-op on %s: Android handles stay-awake natively — enable Settings → Developer options → Stay awake while plugged in", dev))
	default:
		return toolText(fmt.Sprintf("KeepAwake launched on %s", dev))
	}
}

func (h *Handler) handleDeviceState(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	state, err := adapter.State(id)
	if err != nil {
		return toolErr("reading state: %v", err)
	}
	return toolJSON(state)
}

func (h *Handler) handleScreenshot(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if platform == "ios" && h.tunneld != nil {
		if err := h.tunneld.Require(); err != nil {
			return toolErr("screenshot on %s: %v", dev, err)
		}
	}
	png, err := adapter.Screenshot(id)
	if err != nil {
		return toolErr("screenshot on %s: %v", dev, err)
	}
	h.archiveArtefact(dev, owner, "screenshot", "image/png", ".png", png)
	return mcpgo.NewToolResultImage(
		fmt.Sprintf("screenshot of %s (%d bytes)", dev, len(png)),
		base64.StdEncoding.EncodeToString(png),
		"image/png",
	), nil
}

// archiveArtefact writes data into the active run for (device, owner)
// if one exists. Best-effort: missing run store, no active run, or a
// write failure all log and return. The primary tool result is
// authoritative; artefact persistence is observability.
func (h *Handler) archiveArtefact(dev, owner, source, mime, ext string, data []byte) {
	if h.runs == nil {
		return
	}
	canonical := h.canonicalDevice(dev)
	run, err := h.runs.Active(canonical, owner)
	if err != nil {
		slog.Warn("runs: active lookup failed",
			"device", canonical, "owner", owner, "error", err)
		return
	}
	if run == nil {
		return
	}
	name := fmt.Sprintf("%s-%s%s",
		source, time.Now().UTC().Format("20060102-150405"), ext)
	if _, err := h.runs.AddArtefact(run.ID, source, name, mime, data); err != nil {
		slog.Warn("runs: archive artefact failed",
			"run", run.ID, "source", source, "error", err)
	}
}

// canonicalDevice returns the inventory alias for ref when one is
// known, mirroring the reservation normaliser so Active() lookups key
// off the same string regardless of whether the caller passed an
// alias or a raw UDID/serial.
func (h *Handler) canonicalDevice(ref string) string {
	if h.inventory == nil {
		return ref
	}
	if entry, ok := h.inventory.Lookup(ref); ok && entry.Alias != "" {
		return entry.Alias
	}
	return ref
}

func (h *Handler) handleListApps(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	apps, err := adapter.ListApps(id)
	if err != nil {
		return toolErr("list_apps on %s: %v", dev, err)
	}
	return toolJSON(apps)
}

func (h *Handler) handleLaunchApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	bundleID, err := requireString(args, "bundle_id")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	h.mu.Lock()
	defer h.mu.Unlock()
	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if platform == "ios" && h.tunneld != nil {
		if err := h.tunneld.Require(); err != nil {
			return toolErr("launch_app on %s: %v", dev, err)
		}
	}
	if err := adapter.LaunchApp(id, bundleID); err != nil {
		return toolErr("launch_app %s on %s: %v", bundleID, dev, err)
	}
	return toolText(fmt.Sprintf("launched %s on %s", bundleID, dev))
}

func (h *Handler) handleTerminateApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	bundleID, err := requireString(args, "bundle_id")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	h.mu.Lock()
	defer h.mu.Unlock()
	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if platform == "ios" && h.tunneld != nil {
		if err := h.tunneld.Require(); err != nil {
			return toolErr("terminate_app on %s: %v", dev, err)
		}
	}
	if err := adapter.TerminateApp(id, bundleID); err != nil {
		return toolErr("terminate_app %s on %s: %v", bundleID, dev, err)
	}
	return toolText(fmt.Sprintf("terminated %s on %s", bundleID, dev))
}

// authorize checks that the caller (identified by owner) may perform
// a mutating operation against dev. Returns nil if allowed;
// a *mcpgo.CallToolResult with IsError=true otherwise. Passes through
// when no reservation store is wired (tests/embedded use).
func (h *Handler) authorize(dev, owner string) *mcpgo.CallToolResult {
	if h.reservations == nil {
		return nil
	}
	if err := h.reservations.Authorize(dev, owner); err != nil {
		res, _ := toolErr("%v", err)
		return res
	}
	return nil
}

// --- reservation tools -------------------------------------------------

func (h *Handler) handleReserve(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolErr("reservations not configured on this server")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(optNumber(args, "ttl_seconds")) * time.Second
	note := optString(args, "note")

	r, err := h.reservations.Acquire(dev, owner, ttl, note)
	if err != nil {
		return toolErr("%v", err)
	}

	// Ensure a run is open for this (device, owner). Best-effort;
	// reservation acquisition is already committed.
	if h.runs != nil {
		canonical := h.canonicalDevice(dev)
		existing, lerr := h.runs.Active(canonical, owner)
		switch {
		case lerr != nil:
			slog.Warn("runs: active lookup on reserve failed",
				"device", canonical, "owner", owner, "error", lerr)
		case existing == nil:
			if _, err := h.runs.Open(canonical, owner, note); err != nil {
				slog.Warn("runs: open on reserve failed",
					"device", canonical, "owner", owner, "error", err)
			}
		}
	}

	return toolJSON(r)
}

func (h *Handler) handleRelease(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolErr("reservations not configured on this server")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	if err := h.reservations.Release(dev, owner); err != nil {
		return toolErr("%v", err)
	}

	// Close the matching run, if any. Best-effort.
	if h.runs != nil {
		canonical := h.canonicalDevice(dev)
		if run, lerr := h.runs.Active(canonical, owner); lerr == nil && run != nil {
			if cerr := h.runs.Close(run.ID); cerr != nil {
				slog.Warn("runs: close on release failed",
					"run", run.ID, "error", cerr)
			}
		}
	}

	return toolText(fmt.Sprintf("released reservation on %s", dev))
}

func (h *Handler) handleRenew(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolErr("reservations not configured on this server")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(optNumber(args, "ttl_seconds")) * time.Second
	r, err := h.reservations.Renew(dev, owner, ttl)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(r)
}

func (h *Handler) handleReservations(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolJSON([]reservations.Reservation{})
	}
	return toolJSON(h.reservations.List())
}

// --- run-artefact tools --------------------------------------------

func (h *Handler) handleRunsList(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.runs == nil {
		return toolJSON([]runs.Run{})
	}
	list, err := h.runs.List()
	if err != nil {
		return toolErr("%v", err)
	}
	if list == nil {
		list = []runs.Run{}
	}
	return toolJSON(list)
}

func (h *Handler) handleRunsShow(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.runs == nil {
		return toolErr("runs store not configured on this server")
	}
	id, err := requireString(args, "run_id")
	if err != nil {
		return nil, err
	}
	r, err := h.runs.Get(id)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(r)
}

// optNumber extracts a float64-coerced integer-ish value. MCP
// arguments arrive as map[string]any where JSON numbers decode to
// float64; we cast to int64 seconds for time.Duration.
func optNumber(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// resolveAdapter maps a user-provided device reference (alias or raw UUID)
// to the platform adapter, the platform name ("ios" | "android"), and the
// platform-specific identifier it expects. Raw identifiers not in the
// inventory are classified by format (iOS UDID vs. Android serial) via
// inventory.ClassifyRaw.
func (h *Handler) resolveAdapter(ref string) (device.Adapter, string, string, error) {
	entry, ok := h.inventory.Lookup(ref)
	if !ok {
		classified := inventory.ClassifyRaw(ref)
		if classified.Platform == "android" {
			return h.android, "android", ref, nil
		}
		return h.ios, "ios", ref, nil
	}
	switch entry.Platform {
	case "ios":
		id := entry.IOSUUID
		if id == "" {
			id = entry.IOSCoreDevice
		}
		return h.ios, "ios", id, nil
	case "android":
		return h.android, "android", entry.AndroidSerial, nil
	default:
		return nil, "", "", fmt.Errorf("inventory entry %q has unknown platform %q", ref, entry.Platform)
	}
}

// Compile-time assertion that errors package is imported (for build).
var _ = errors.New
