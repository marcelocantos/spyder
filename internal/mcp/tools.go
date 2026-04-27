// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/network"
	"github.com/marcelocantos/spyder/internal/recording"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/runs"
	"github.com/marcelocantos/spyder/internal/selector"
	"github.com/marcelocantos/spyder/internal/simemu"
)

// handleLogsRange returns log lines between since and until for a device.
// Read-only; not subject to reservation checks.
func (h *Handler) handleLogsRange(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}

	filter := device.LogFilter{
		Process:   optString(args, "process"),
		Subsystem: optString(args, "subsystem"),
		Tag:       optString(args, "tag"),
		Regex:     optString(args, "regex"),
	}

	var since, until time.Time
	if s := optString(args, "since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return toolErr("since: invalid RFC3339 timestamp: %v", err)
		}
		since = t
	}
	if u := optString(args, "until"); u != "" {
		t, err := time.Parse(time.RFC3339, u)
		if err != nil {
			return toolErr("until: invalid RFC3339 timestamp: %v", err)
		}
		until = t
	}

	h.mu.Lock()
	adapter, _, id, adapterErr := h.resolveAdapter(dev)
	h.mu.Unlock()

	if adapterErr != nil {
		return toolErr("%v", adapterErr)
	}

	lines, err := adapter.LogRange(id, filter, since, until)
	if err != nil {
		return toolErr("log range on %s: %v", dev, err)
	}
	if lines == nil {
		lines = []device.LogLine{}
	}
	return toolJSON(lines)
}

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
	name := optString(args, "name")
	selRaw := optString(args, "selector")

	if name == "" && selRaw == "" {
		return nil, fmt.Errorf("name or selector is required")
	}
	if name != "" && selRaw != "" {
		return toolErr("resolve: supply either name or selector, not both")
	}

	// Selector path: resolve to the first matching live device, then
	// project back to its inventory entry (or a synthetic entry built
	// from the live device.Info).
	if selRaw != "" {
		var sel selector.Selector
		if err := json.Unmarshal([]byte(selRaw), &sel); err != nil {
			return toolErr("resolve: invalid selector JSON: %v", err)
		}
		if sel.Platform == "" {
			return toolErr("resolve: selector.platform is required")
		}

		h.mu.Lock()
		defer h.mu.Unlock()

		candidates := h.buildCandidates(sel.Platform)
		info, err := selector.Resolve(sel, candidates, h.pool)
		if err != nil {
			return toolErr("resolve: %v", err)
		}
		entry, ok := h.inventory.Lookup(info.UUID)
		if !ok {
			// Fall back to a synthetic entry from the live device.Info.
			entry = inventory.Entry{
				Alias:    info.Alias,
				Platform: info.Platform,
			}
			switch info.Platform {
			case "ios":
				entry.IOSUUID = info.UUID
			case "android":
				entry.AndroidSerial = info.UUID
			}
		}
		return toolJSON(entry)
	}

	// Name path (existing behaviour): inventory lookup with raw fallback.
	h.mu.Lock()
	defer h.mu.Unlock()

	entry, ok := h.inventory.Lookup(name)
	if !ok {
		entry = inventory.ClassifyRaw(name)
	}
	return toolJSON(entry)
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
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
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
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if err := adapter.LaunchApp(id, bundleID); err != nil {
		return toolErr("launch_app %s on %s: %v", bundleID, dev, err)
	}
	if h.awake != nil {
		// Tell autoawake's convergence loop that spyder itself just
		// foregrounded another bundle on this device. Without this, the
		// next tick's KeepAwake state probe would see Running → background-
		// ed and misread it as user opt-out (🎯T48).
		h.awake.NoteAppLaunched(id, bundleID)
	}
	return toolText(fmt.Sprintf("launched %s on %s", bundleID, dev))
}

// isRunningResult is the structured response of the is_running tool.
// state is one of "running", "not_running", or "not_installed". pid is
// populated only when state == "running".
type isRunningResult struct {
	State string `json:"state"`
	PID   int    `json:"pid,omitempty"`
}

// handleIsRunning answers "is this app currently running on this device?"
// without forcing a launch (distinct from launch_app's PID-verify).
// Read-only; not subject to reservations. Used by ge's smoke-test
// scripts to passively check app state. (🎯T38.1)
func (h *Handler) handleIsRunning(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	bundleID, err := requireString(args, "bundle_id")
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}

	// Try AppPID first — fast and authoritative for "running".
	if pid, pidErr := adapter.AppPID(id, bundleID); pidErr == nil && pid > 0 {
		return toolJSON(isRunningResult{State: "running", PID: pid})
	}

	// Not running — distinguish "not installed" from "not running" via
	// ListApps. Some adapters return errors that already indicate
	// installation state; the ListApps cross-check is the universal
	// source of truth.
	apps, listErr := adapter.ListApps(id)
	if listErr != nil {
		return toolErr("is_running: list_apps on %s: %v", dev, listErr)
	}
	for _, app := range apps {
		if app.BundleID == bundleID {
			return toolJSON(isRunningResult{State: "not_running"})
		}
	}
	return toolJSON(isRunningResult{State: "not_installed"})
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
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
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

// resolveReserveDevice resolves the concrete device reference from the
// reserve args. Returns the canonical device string (alias or UUID) to
// pass to Acquire, or an error result when args are invalid.
func (h *Handler) resolveReserveDevice(args map[string]any) (string, *mcpgo.CallToolResult) {
	devStr := optString(args, "device")
	selRaw := optString(args, "selector")

	if devStr != "" && selRaw != "" {
		res, _ := toolErr("reserve: supply either device or selector, not both")
		return "", res
	}
	if devStr == "" && selRaw == "" {
		res, _ := toolErr("reserve: one of device or selector is required")
		return "", res
	}

	if devStr != "" {
		return devStr, nil
	}

	// Parse selector JSON.
	var sel selector.Selector
	if err := json.Unmarshal([]byte(selRaw), &sel); err != nil {
		res, _ := toolErr("reserve: invalid selector JSON: %v", err)
		return "", res
	}
	if sel.Platform == "" {
		res, _ := toolErr("reserve: selector.platform is required")
		return "", res
	}

	// Build candidate list from live devices + inventory entries.
	candidates := h.buildCandidates(sel.Platform)

	info, err := selector.Resolve(sel, candidates, h.pool)
	if err != nil {
		res, _ := toolErr("reserve: %v", err)
		return "", res
	}

	// Determine the canonical reference: alias if known, else UUID.
	if alias := h.inventory.AliasFor(info.UUID); alias != "" {
		return alias, nil
	}
	return info.UUID, nil
}

// buildCandidates assembles a []selector.Candidate from the live device
// set (for the given platform) combined with inventory entries.
// Errors from List() are silently dropped — we use whatever is available.
func (h *Handler) buildCandidates(platform string) []selector.Candidate {
	// Fetch live device list.
	var liveDevices []device.Info
	fetchForPlatform := func(p string, adapter device.Adapter) {
		if platform != "all" && !strings.EqualFold(p, platform) {
			return
		}
		ds, err := adapter.List()
		if err != nil {
			return
		}
		liveDevices = append(liveDevices, ds...)
	}
	fetchForPlatform("ios", h.ios)
	fetchForPlatform("android", h.android)

	// Index inventory entries by UUID for quick join.
	entries := h.inventory.Entries()
	entryByUUID := make(map[string]inventory.Entry, len(entries))
	for _, e := range entries {
		if e.IOSUUID != "" {
			entryByUUID[e.IOSUUID] = e
		}
		if e.IOSCoreDevice != "" {
			entryByUUID[e.IOSCoreDevice] = e
		}
		if e.AndroidSerial != "" {
			entryByUUID[e.AndroidSerial] = e
		}
	}

	var candidates []selector.Candidate
	for _, info := range liveDevices {
		e := entryByUUID[info.UUID]
		// Annotate alias from inventory.
		if info.Alias == "" && e.Alias != "" {
			info.Alias = e.Alias
		}
		isSimOrEmu := isSimulatorOrEmulator(info)
		isReserved := false
		if h.reservations != nil {
			ref := info.UUID
			if info.Alias != "" {
				ref = info.Alias
			}
			_, isReserved = h.reservations.Get(ref)
		}
		candidates = append(candidates, selector.Candidate{
			Info:       info,
			Entry:      e,
			IsSimOrEmu: isSimOrEmu,
			IsReserved: isReserved,
		})
	}
	return candidates
}

// isSimulatorOrEmulator returns true when the device's UUID or model
// suggests it is a simulator or emulator (not a physical device).
//
// Heuristics:
//   - iOS: simctl UDIDs are standard UUIDs (8-4-4-4-12 hex). Physical iOS
//     hardware UDIDs have the form XXXXXXXX-XXXXXXXXXXXXXXXX (8 then 16 hex).
//   - Android: emulator serials start with "emulator-".
func isSimulatorOrEmulator(info device.Info) bool {
	switch strings.ToLower(info.Platform) {
	case "ios":
		// Standard UUID → simulator. iOS hardware UDID has 8+16 hex groups.
		return looksLikeStandardUUID(info.UUID)
	case "android":
		return strings.HasPrefix(strings.ToLower(info.UUID), "emulator-")
	}
	return false
}

// looksLikeStandardUUID returns true when s matches the 8-4-4-4-12 hex UUID form.
func looksLikeStandardUUID(s string) bool {
	// Quick length check: 8+1+4+1+4+1+4+1+12 = 36.
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !isHexRune(c) {
			return false
		}
	}
	return true
}

func isHexRune(r rune) bool {
	return (r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'f') ||
		(r >= 'A' && r <= 'F')
}

func (h *Handler) handleReserve(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolErr("reservations not configured on this server")
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(optNumber(args, "ttl_seconds")) * time.Second
	note := optString(args, "note")

	h.mu.Lock()
	dev, errRes := h.resolveReserveDevice(args)
	h.mu.Unlock()
	if errRes != nil {
		return errRes, nil
	}

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

	h.mu.Lock()
	canonical := h.canonicalDevice(dev)
	// Stop any active recording owned by this owner before releasing.
	h.stopRecordingForOwner(canonical, owner)
	h.mu.Unlock()

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

	// Best-effort network clear: if this owner applied a network profile
	// on this device, clear it on reservation release.  Errors are
	// surfaced in the result text but do not fail the release.  This
	// covers the normal-exit path; if the daemon dies before release the
	// emulator retains the applied profile until the next explicit clear
	// or emulator restart (documented in the tool description).
	h.mu.Lock()
	applied, hasProfile := h.networkByDevice[dev]
	if hasProfile && applied.owner == owner {
		delete(h.networkByDevice, dev)
		// Resolve the adapter while still under the lock (same pattern
		// as all other handler methods).
		adapter, _, id, resolveErr := h.resolveAdapter(dev)
		h.mu.Unlock()
		if resolveErr == nil {
			if clearErr := adapter.ClearNetwork(id); clearErr != nil {
				return toolText(fmt.Sprintf(
					"released reservation on %s (network clear failed: %v — clear manually if needed)",
					dev, clearErr,
				))
			}
		}
	} else {
		h.mu.Unlock()
	}

	return toolText(fmt.Sprintf("released reservation on %s", dev))
}

// handleNetwork applies or clears a network condition profile on a device.
// Requires an active reservation from the caller (owner field).
//
// Arguments:
//
//	device  — required; device alias or UUID.
//	owner   — required; must match the current reservation holder.
//	profile — optional; named or dynamic profile string. Mutually exclusive with clear.
//	clear   — optional bool; if true, clears any applied profile. Mutually exclusive with profile.
//
// Exactly one of profile or clear must be supplied.
func (h *Handler) handleNetwork(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}

	profileName := optString(args, "profile")
	clearFlag, _ := args["clear"].(bool)

	if profileName == "" && !clearFlag {
		return toolErr("network: supply either profile=<name> or clear=true")
	}
	if profileName != "" && clearFlag {
		return toolErr("network: profile and clear are mutually exclusive")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}

	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}

	if clearFlag {
		if err := adapter.ClearNetwork(id); err != nil {
			return toolErr("clear network on %s: %v", dev, err)
		}
		delete(h.networkByDevice, dev)
		return toolText(fmt.Sprintf("network conditions cleared on %s", dev))
	}

	// Apply profile.
	p, err := network.Parse(profileName)
	if err != nil {
		return toolErr("%v", err)
	}

	if applyErr := adapter.ApplyNetwork(id, p); applyErr != nil {
		return toolErr("apply network %q on %s: %v", profileName, dev, applyErr)
	}

	h.networkByDevice[dev] = appliedNetwork{profile: p, owner: owner}
	return toolText(fmt.Sprintf("network profile %q applied on %s", profileName, dev))
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

// --------------------------------------------------------------------------
// iOS simulator tools
// --------------------------------------------------------------------------

// handleSimList lists all iOS simulators known to simctl, optionally
// filtered by state ("Booted", "Shutdown", etc.). Also includes
// available device types and runtimes when requested.
func (h *Handler) handleSimList(args map[string]any) (*mcpgo.CallToolResult, error) {
	state := optString(args, "state")
	devices, err := simemu.SimList()
	if err != nil {
		return toolErr("sim_list: %v", err)
	}
	if state != "" {
		filtered := devices[:0]
		for _, d := range devices {
			if d.State == state {
				filtered = append(filtered, d)
			}
		}
		devices = filtered
	}
	if devices == nil {
		devices = []simemu.SimDevice{}
	}
	return toolJSON(devices)
}

func (h *Handler) handleSimCreate(args map[string]any) (*mcpgo.CallToolResult, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	deviceTypeID, err := requireString(args, "device_type_id")
	if err != nil {
		return nil, err
	}
	runtimeID, err := requireString(args, "runtime_id")
	if err != nil {
		return nil, err
	}
	udid, err := simemu.SimCreate(name, deviceTypeID, runtimeID)
	if err != nil {
		return toolErr("sim_create: %v", err)
	}
	return toolJSON(map[string]string{"udid": udid, "name": name})
}

func (h *Handler) handleSimBoot(args map[string]any) (*mcpgo.CallToolResult, error) {
	udid, err := requireString(args, "udid")
	if err != nil {
		return nil, err
	}
	if err := simemu.SimBoot(udid); err != nil {
		return toolErr("sim_boot: %v", err)
	}
	return toolText(fmt.Sprintf("simulator %s booted", udid))
}

func (h *Handler) handleSimShutdown(args map[string]any) (*mcpgo.CallToolResult, error) {
	udid, err := requireString(args, "udid")
	if err != nil {
		return nil, err
	}
	if err := simemu.SimShutdown(udid); err != nil {
		return toolErr("sim_shutdown: %v", err)
	}
	return toolText(fmt.Sprintf("simulator %s shut down", udid))
}

func (h *Handler) handleSimDelete(args map[string]any) (*mcpgo.CallToolResult, error) {
	udid, err := requireString(args, "udid")
	if err != nil {
		return nil, err
	}
	if err := simemu.SimDelete(udid); err != nil {
		return toolErr("sim_delete: %v", err)
	}
	return toolText(fmt.Sprintf("simulator %s deleted", udid))
}

// --------------------------------------------------------------------------
// Android emulator tools
// --------------------------------------------------------------------------

func (h *Handler) handleEmuList(_ map[string]any) (*mcpgo.CallToolResult, error) {
	avds, err := simemu.AVDList()
	if err != nil {
		return toolErr("emu_list: %v", err)
	}
	if avds == nil {
		avds = []simemu.AVD{}
	}
	return toolJSON(avds)
}

func (h *Handler) handleEmuCreate(args map[string]any) (*mcpgo.CallToolResult, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	systemImage, err := requireString(args, "system_image")
	if err != nil {
		return nil, err
	}
	deviceProfile, err := requireString(args, "device_profile")
	if err != nil {
		return nil, err
	}
	if err := simemu.AVDCreate(name, systemImage, deviceProfile); err != nil {
		return toolErr("emu_create: %v", err)
	}
	return toolText(fmt.Sprintf("AVD %q created", name))
}

func (h *Handler) handleEmuBoot(args map[string]any) (*mcpgo.CallToolResult, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	msg, err := simemu.AVDBoot(name)
	if err != nil {
		return toolErr("emu_boot: %v", err)
	}
	return toolText(msg)
}

func (h *Handler) handleEmuShutdown(args map[string]any) (*mcpgo.CallToolResult, error) {
	serial, err := requireString(args, "serial")
	if err != nil {
		return nil, err
	}
	if err := simemu.AVDShutdown(serial); err != nil {
		return toolErr("emu_shutdown: %v", err)
	}
	return toolText(fmt.Sprintf("emulator %s shut down", serial))
}

func (h *Handler) handleEmuDelete(args map[string]any) (*mcpgo.CallToolResult, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	if err := simemu.AVDDelete(name); err != nil {
		return toolErr("emu_delete: %v", err)
	}
	return toolText(fmt.Sprintf("AVD %q deleted", name))
}

func (h *Handler) handleRotate(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	orientation, err := requireString(args, "orientation")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if err := adapter.Rotate(id, orientation); err != nil {
		return toolErr("rotate %s on %s: %v", orientation, dev, err)
	}
	return toolText(fmt.Sprintf("rotated %s to %s", dev, orientation))
}

// handleCrashes fetches crash reports from a device. Read-only; not
// reservation-gated (same pattern as device_state). Optionally archives
// reports into the active run when an owner is provided.
func (h *Handler) handleCrashes(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	var since time.Time
	if s := optString(args, "since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return toolErr("since: invalid RFC3339 timestamp %q: %v", s, err)
		}
		since = t
	}
	process := optString(args, "process")

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}

	reports, err := adapter.Crashes(id, since, process)
	if err != nil {
		return toolErr("crashes on %s: %v", dev, err)
	}

	// Optionally archive each pulled .ips report into the active run.
	if h.runs != nil && owner != "" {
		for _, r := range reports {
			if r.Raw == "" {
				continue
			}
			h.archiveArtefact(dev, owner, "crashes", "application/x-apple-crashreport", ".ips", []byte(r.Raw))
		}
	}

	return toolJSON(reports)
}

// --- recording tools ---------------------------------------------------

// handleRecordStart begins a screen recording on the device. The recording
// runs asynchronously; the caller must invoke record_stop to finalise.
func (h *Handler) handleRecordStart(args map[string]any) (*mcpgo.CallToolResult, error) {
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
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}

	canonical := h.canonicalDevice(dev)

	// Conflict check: only one active recorder per device.
	if existing := h.recordings.ForDevice(canonical); existing != nil {
		return toolErr("record_start: device %q is already being recorded by owner %q — call record_stop first", dev, existing.Owner)
	}

	// Build output path in the temp/run dir.
	dir := h.runsBaseDir
	if dir == "" {
		dir = os.TempDir()
	}
	ts := time.Now().UTC().Format("20060102-150405")
	dest := filepath.Join(dir, fmt.Sprintf("recording-%s-%s.mp4", sanitizeFilename(canonical), ts))

	stopFn, pid, err := adapter.StartRecording(id, dest)
	if err != nil {
		return toolErr("record_start on %s: %v", dev, err)
	}

	doneCh := make(chan struct{})
	wrappedStop := func() error {
		defer close(doneCh)
		return stopFn()
	}

	if _, err := h.recordings.Start(canonical, owner, dest, wrappedStop, doneCh); err != nil {
		// Should not happen because we checked ForDevice above, but be safe.
		return toolErr("record_start: %v", err)
	}

	slog.Info("recording started", "device", canonical, "owner", owner, "pid", pid, "dest", dest)
	return toolText(fmt.Sprintf("recording started on %s (pid %d); call record_stop to finalise → %s", dev, pid, dest))
}

// handleRecordStop signals the active recorder to stop, waits for the
// mp4 to be finalised, and returns the output path.
func (h *Handler) handleRecordStop(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	h.mu.Lock()

	if res := h.authorize(dev, owner); res != nil {
		h.mu.Unlock()
		return res, nil
	}
	canonical := h.canonicalDevice(dev)

	session, stopErr := h.recordings.Stop(canonical)
	h.mu.Unlock()

	if stopErr != nil {
		return toolErr("record_stop on %s: %v", dev, stopErr)
	}

	// Wait for the subprocess to exit and the file to be written.
	select {
	case <-session.Done():
	case <-time.After(30 * time.Second):
		return toolErr("record_stop on %s: timed out waiting for recorder to exit", dev)
	}

	slog.Info("recording stopped", "device", canonical, "output", session.OutputPath)
	return toolText(fmt.Sprintf("recording saved to %s", session.OutputPath))
}

// stopRecordingForOwner stops any active recording on device owned by owner.
// Called from handleRelease to clean up before releasing the reservation.
// Best-effort: errors are logged but not returned.
func (h *Handler) stopRecordingForOwner(canonical, owner string) {
	session := h.recordings.ForDevice(canonical)
	if session == nil || session.Owner != owner {
		return
	}
	s, err := h.recordings.Stop(canonical)
	if err != nil {
		slog.Warn("recording cleanup on release failed", "device", canonical, "owner", owner, "error", err)
		return
	}
	// Wait briefly for clean shutdown.
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		slog.Warn("recording cleanup timed out", "device", canonical, "owner", owner)
	}
}

// sanitizeFilename replaces characters that are unsafe in file names.
func sanitizeFilename(s string) string {
	out := make([]byte, len(s))
	for i := range len(s) {
		c := s[i]
		if c == '/' || c == '\\' || c == ':' || c == '*' || c == '?' || c == '"' || c == '<' || c == '>' || c == '|' {
			out[i] = '_'
		} else {
			out[i] = c
		}
	}
	return string(out)
}

// Compile-time check: recording.IsConflict must be callable.
var _ = recording.IsConflict

// validateAppPath rejects paths with ".." traversal components and
// paths that do not exist. Returns the cleaned absolute path.
func validateAppPath(path string) (string, error) {
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("path must not contain '..': %q", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("path does not exist: %q", abs)
	}
	return abs, nil
}

func (h *Handler) handleInstallApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	path, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	path, err = validateAppPath(path)
	if err != nil {
		return toolErr("%v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if err := adapter.InstallApp(id, path); err != nil {
		return toolErr("install_app on %s: %v", dev, err)
	}
	return toolText(fmt.Sprintf("installed %s on %s", filepath.Base(path), dev))
}

func (h *Handler) handleUninstallApp(args map[string]any) (*mcpgo.CallToolResult, error) {
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
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if err := adapter.UninstallApp(id, bundleID); err != nil {
		return toolErr("uninstall_app on %s: %v", dev, err)
	}
	return toolText(fmt.Sprintf("uninstalled %s from %s", bundleID, dev))
}

// deployResult is the JSON payload returned by deploy_app on success.
type deployResult struct {
	BundleID string `json:"bundle_id"`
	PID      int    `json:"pid"`
}

func (h *Handler) handleDeployApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	path, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	bundleID := optString(args, "bundle_id")
	owner := optString(args, "owner")

	path, err = validateAppPath(path)
	if err != nil {
		return toolErr("%v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}

	// Derive bundle id from the path if not supplied.
	if bundleID == "" {
		var deriveErr error
		bundleID, deriveErr = deriveBundleID(platform, path)
		if deriveErr != nil {
			return toolErr("cannot derive bundle_id from %q: %v — pass --bundle-id explicitly", path, deriveErr)
		}
	}

	// Step 1: terminate (ignore "not running" errors — it's fine if the
	// app isn't already up; the important thing is we tried).
	if termErr := adapter.TerminateApp(id, bundleID); termErr != nil {
		// Only propagate if the error is not "not running" or "not found".
		if !isNotRunningError(termErr) {
			return toolErr("deploy_app: terminate %s on %s: %v", bundleID, dev, termErr)
		}
	}

	// Step 2: install (fail fast on error).
	if err := adapter.InstallApp(id, path); err != nil {
		return toolErr("deploy_app: install %s on %s: %v", filepath.Base(path), dev, err)
	}

	// Step 3: launch.
	if err := adapter.LaunchApp(id, bundleID); err != nil {
		return toolErr("deploy_app: launch %s on %s: %v", bundleID, dev, err)
	}
	if h.awake != nil {
		// Same opt-out-suppression hook as handleLaunchApp (🎯T48).
		h.awake.NoteAppLaunched(id, bundleID)
	}

	// Step 4: verify new PID.
	pid, err := adapter.AppPID(id, bundleID)
	if err != nil {
		return toolErr("deploy_app: verify pid for %s on %s: %v", bundleID, dev, err)
	}

	return toolJSON(deployResult{BundleID: bundleID, PID: pid})
}

// isNotRunningError returns true for "app not running" / "not installed"
// errors from TerminateApp. These are safe to ignore during a deploy.
func isNotRunningError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not running") ||
		strings.Contains(s, "app not running") ||
		strings.Contains(s, "not installed") ||
		strings.Contains(s, "not found")
}

// deriveBundleID extracts the CFBundleIdentifier from an iOS .app bundle's
// Info.plist, or the package name from an Android .apk via aapt (with a
// --bundle-id fallback message when aapt is absent).
func deriveBundleID(platform, path string) (string, error) {
	switch platform {
	case "ios":
		return iosBundleIDFromApp(path)
	case "android":
		return androidBundleIDFromAPK(path)
	default:
		return "", fmt.Errorf("unsupported platform %q", platform)
	}
}

// iosBundleIDFromApp reads CFBundleIdentifier from <path>/Info.plist.
// The plist is expected in its JSON-serialised form as produced by
// xcrun plutil or after xcrun devicectl install; for raw binary plists
// we shell out to plutil -convert json.
func iosBundleIDFromApp(appPath string) (string, error) {
	plistPath := filepath.Join(appPath, "Info.plist")
	if _, err := os.Stat(plistPath); err != nil {
		return "", fmt.Errorf("Info.plist not found in %q", appPath)
	}
	// Convert to JSON then parse — handles both binary and XML plists.
	var outBuf, errBuf bytes.Buffer
	cmd := exec.Command("plutil", "-convert", "json", "-o", "-", plistPath)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("plutil convert Info.plist: %w", err)
	}
	var plist struct {
		CFBundleIdentifier string `json:"CFBundleIdentifier"`
	}
	if err := json.Unmarshal(outBuf.Bytes(), &plist); err != nil {
		return "", fmt.Errorf("parse Info.plist JSON: %w", err)
	}
	if plist.CFBundleIdentifier == "" {
		return "", fmt.Errorf("CFBundleIdentifier not set in Info.plist")
	}
	return plist.CFBundleIdentifier, nil
}

// androidBundleIDFromAPK extracts the package name from an APK via
// `aapt dump badging`. Falls back to a clear error when aapt is absent.
func androidBundleIDFromAPK(apkPath string) (string, error) {
	var outBuf bytes.Buffer
	cmd := exec.Command("aapt", "dump", "badging", apkPath)
	cmd.Stdout = &outBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("aapt dump badging: %w (is aapt in PATH? install Android SDK build-tools)", err)
	}
	for _, line := range strings.Split(outBuf.String(), "\n") {
		if !strings.HasPrefix(line, "package:") {
			continue
		}
		// package: name='com.example.app' versionCode=... versionName=...
		for _, field := range strings.Fields(line) {
			if after, ok := strings.CutPrefix(field, "name='"); ok {
				return strings.TrimSuffix(after, "'"), nil
			}
		}
	}
	return "", fmt.Errorf("package name not found in aapt output")
}

// Compile-time assertion that errors package is imported (for build).
var _ = errors.New

// --------------------------------------------------------------------------
// Pool tools (🎯T24)
// --------------------------------------------------------------------------

func (h *Handler) handlePoolList(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.poolMgr == nil {
		return toolErr("pool not configured — create ~/.spyder/pool.yaml and restart the daemon")
	}
	return toolJSON(h.poolMgr.PoolStatus())
}

func (h *Handler) handlePoolWarm(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.poolMgr == nil {
		return toolErr("pool not configured — create ~/.spyder/pool.yaml and restart the daemon")
	}
	tmpl, err := requireString(args, "template")
	if err != nil {
		return nil, err
	}
	countRaw, _ := args["count"].(float64)
	count := int(countRaw)
	if count <= 0 {
		return toolErr("count must be a positive integer")
	}
	if err := h.poolMgr.PoolWarm(tmpl, count); err != nil {
		return toolErr("pool warm %q: %v", tmpl, err)
	}
	return toolText(fmt.Sprintf("warming %d instance(s) for template %q", count, tmpl))
}

func (h *Handler) handlePoolDrain(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.poolMgr == nil {
		return toolErr("pool not configured — create ~/.spyder/pool.yaml and restart the daemon")
	}
	tmpl, err := requireString(args, "template")
	if err != nil {
		return nil, err
	}
	if err := h.poolMgr.PoolDrain(tmpl); err != nil {
		return toolErr("pool drain %q: %v", tmpl, err)
	}
	return toolText(fmt.Sprintf("drained all idle instances for template %q", tmpl))
}
