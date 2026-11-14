// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/simlease"
)

// Simulator leases (🎯T188): spyder alone boots and shuts down iOS
// simulators. sim_acquire returns a UDID under an expiring lease;
// sim_release (or expiry) undoes exactly what the acquire did.

// SetSimLeases attaches the lease manager and turns on simulator
// discovery (the booted-simulator census behind `devices` and UDID
// routing). Called by the daemon before serving.
func (h *Handler) SetSimLeases(m *simlease.Manager) {
	h.mu.Lock()
	h.simLeases = m
	if h.bootedSims == nil {
		h.bootedSims = device.BootedSimulators
	}
	h.mu.Unlock()
}

// RunSimLeaseReaper releases expired simulator leases until ctx ends,
// starting with any left over from before a restart.
func (h *Handler) RunSimLeaseReaper(ctx context.Context) {
	h.mu.Lock()
	m := h.simLeases
	h.mu.Unlock()
	if m != nil {
		m.Run(ctx, simlease.ReapInterval)
	}
}

func (h *Handler) leases() (*simlease.Manager, *mcpgo.CallToolResult) {
	h.mu.Lock()
	m := h.simLeases
	h.mu.Unlock()
	if m == nil {
		res, _ := toolErr("simulator leases not configured on this server")
		return nil, res
	}
	return m, nil
}

func (h *Handler) handleSimAcquire(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleSimAcquireContext(context.Background(), args)
}

func (h *Handler) handleSimAcquireContext(ctx context.Context, args map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	deviceType, err := requireString(args, "device_type")
	if err != nil {
		return nil, err
	}
	l, err := m.Acquire(ctx, simlease.AcquireArgs{
		DeviceType: deviceType,
		Runtime:    optString(args, "runtime"),
		Owner:      owner,
		Project:    optString(args, "project"),
		Name:       optString(args, "name"),
		TTL:        time.Duration(optNumber(args, "ttl_s")) * time.Second,
	})
	if err != nil {
		return toolErr("%v", err)
	}
	h.forgetSimCache()
	return toolJSON(l)
}

func (h *Handler) handleSimRelease(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleSimReleaseContext(context.Background(), args)
}

func (h *Handler) handleSimReleaseContext(ctx context.Context, args map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	ref := leaseRef(args)
	if ref == "" {
		return toolErr("sim_release: lease_id or udid is required")
	}
	r, err := m.Release(ctx, ref, owner)
	if err != nil {
		return toolErr("%v", err)
	}
	h.forgetSimCache()
	return toolJSON(r)
}

func (h *Handler) handleSimRenew(args map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	ref := leaseRef(args)
	if ref == "" {
		return toolErr("sim_renew: lease_id or udid is required")
	}
	l, err := m.Renew(ref, owner, time.Duration(optNumber(args, "ttl_s"))*time.Second)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(l)
}

func (h *Handler) handleSimLeases(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleSimLeasesContext(context.Background(), args)
}

func (h *Handler) handleSimLeasesContext(ctx context.Context, _ map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	return toolJSON(m.Status(ctx))
}

// --- inventory, claims and prune (🎯T189) ----------------------------

func (h *Handler) handleSimInventory(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleSimInventoryContext(context.Background(), args)
}

func (h *Handler) handleSimInventoryContext(ctx context.Context, _ map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	return toolJSON(m.Inventory(ctx))
}

// defaultPruneIdleDays applies when sim_prune is given no idle_days.
const defaultPruneIdleDays = 7

func pruneArgs(args map[string]any) (simlease.PruneArgs, error) {
	idle := float64(defaultPruneIdleDays)
	if _, ok := args["idle_days"]; ok {
		idle = optFloat(args, "idle_days")
	}
	pa := simlease.PruneArgs{
		IdleDays:      idle,
		DryRun:        optBoolArg(args, "dry_run", true),
		Owner:         optString(args, "owner"),
		IncludeNewest: optBoolArg(args, "include_newest", false),
	}
	if !optBoolArg(args, "unowned", true) {
		return pa, fmt.Errorf("only unowned devices can be pruned (unowned=true)")
	}
	return pa, nil
}

func (h *Handler) handleSimPrune(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleSimPruneContext(context.Background(), args)
}

func (h *Handler) handleSimPruneContext(ctx context.Context, args map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	pa, err := pruneArgs(args)
	if err != nil {
		return toolErr("sim_prune: %v", err)
	}
	r, err := m.PruneDevices(ctx, pa)
	if err != nil {
		return toolErr("%v", err)
	}
	h.forgetSimCache()
	return toolJSON(r)
}

func (h *Handler) handleSimPruneRuntimes(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleSimPruneRuntimesContext(context.Background(), args)
}

func (h *Handler) handleSimPruneRuntimesContext(ctx context.Context, args map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	pa, err := pruneArgs(args)
	if err != nil {
		return toolErr("sim_prune_runtimes: %v", err)
	}
	r, err := m.PruneRuntimes(ctx, pa)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(r)
}

func (h *Handler) handleSimClaim(args map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	id, err := requireString(args, "id")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	p, err := m.Claim(simlease.ClaimArgs{ID: id, Owner: owner, Project: optString(args, "project"), Note: optString(args, "note")})
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(p)
}

func (h *Handler) handleSimUnclaim(args map[string]any) (*mcpgo.CallToolResult, error) {
	m, res := h.leases()
	if res != nil {
		return res, nil
	}
	id, err := requireString(args, "id")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	p, err := m.Unclaim(id, owner)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(p)
}

func leaseRef(args map[string]any) string {
	if id := optString(args, "lease_id"); id != "" {
		return id
	}
	if u := optString(args, "udid"); u != "" {
		return u
	}
	return optString(args, "device")
}

// --- routing simulator UDIDs to the simctl adapter -------------------

// simUUID matches the 8-4-4-4-12 UUID shape CoreSimulator uses. A
// physical iPhone's UDID is 8-16 and never matches.
var simUUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// bootedSimTTL is how long the booted-simulator census is reused when
// deciding whether a UUID names a simulator.
const bootedSimTTL = 5 * time.Second

type simCache struct {
	mu  sync.Mutex
	at  time.Time
	ids map[string]bool
}

// ForgetSimulatorCache drops the booted-simulator census so the next
// lookup sees a just-released (or just-booted) simulator.
func (h *Handler) ForgetSimulatorCache() { h.forgetSimCache() }

func (h *Handler) forgetSimCache() {
	h.simKnown.mu.Lock()
	h.simKnown.at = time.Time{}
	h.simKnown.mu.Unlock()
}

// bootedSimList returns booted simulators, or nil when simulator
// discovery is off (hermetic tests).
func (h *Handler) bootedSimList() []device.Info {
	if h.bootedSims == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectedListTimeout)
	defer cancel()
	infos, _ := h.bootedSims(ctx)
	return infos
}

// isSimulator reports whether ref names a leased or booted simulator.
// Leased UDIDs are always simulators; otherwise a UUID-shaped ref is
// checked against a short-lived census of booted simulators.
func (h *Handler) isSimulator(ref string) bool {
	if !simUUID.MatchString(ref) {
		return false
	}
	if h.simLeases != nil && h.simLeases.Leased(ref) {
		return true
	}
	if h.bootedSims == nil {
		return false
	}
	c := &h.simKnown
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) > bootedSimTTL {
		c.ids = map[string]bool{}
		for _, d := range h.bootedSimList() {
			c.ids[strings.ToUpper(d.UUID)] = true
		}
		c.at = time.Now()
	}
	return c.ids[strings.ToUpper(ref)]
}

// touchSim renews a lease on udid because the device was used.
func (h *Handler) touchSim(udid string) {
	if h.simLeases != nil && udid != "" {
		h.simLeases.Touch(udid)
	}
}

// --- tool definitions ----------------------------------------------------

func simLeaseDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("sim_acquire",
			mcpgo.WithDescription("Lease an iOS simulator (🎯T188) — the way to get a booted simulator; never `xcrun simctl boot` one yourself. spyder reuses a matching simulator or creates one, boots it if needed, and returns {id, udid, name, action, expires_at, ...}. `action` records what spyder did: `created`, `booted`, or `already_running`; sim_release (or expiry) undoes exactly that — deletes a created simulator, shuts down a booted one, leaves an already-running one alone. The lease expires after ttl_s unless renewed by sim_renew or by any spyder verb that uses the UDID (ensure_session, launch_app, screenshot, app_* on its session, ...). The same owner re-acquiring the same device_type/runtime/name gets its existing lease back, renewed. The UDID works directly with devices, ensure_session, launch_app, install_app, terminate_app and screenshot."),
			mcpgo.WithString("device_type", mcpgo.Required(),
				mcpgo.Description("simctl device type: identifier (com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro) or name (\"iPhone 16 Pro\")")),
			mcpgo.WithString("runtime",
				mcpgo.Description("simctl runtime: identifier, name (\"iOS 18.6\") or version (\"18.6\"). Omit for the newest available iOS runtime.")),
			mcpgo.WithString("owner", mcpgo.Required(),
				mcpgo.Description("Who holds the lease; required again to renew or release")),
			mcpgo.WithString("project",
				mcpgo.Description("Repository or project the simulator is for (the CLI fills it from the git toplevel). Recorded so disk use can be blamed on it (🎯T189).")),
			mcpgo.WithString("name",
				mcpgo.Description("Optional simulator name: reuse only a simulator with this name, and give a created one this name. Omit to reuse any match; created ones are named spyder-lease-<id>.")),
			mcpgo.WithNumber("ttl_s",
				mcpgo.Description("Lease length in seconds (default 1800, min 60, max 43200). Activity on the UDID renews it.")),
		),
		mcpgo.NewTool("sim_release",
			mcpgo.WithDescription("End a simulator lease and undo what sim_acquire did: delete a created simulator, shut down a booted one, leave an already-running one alone. Returns {lease, undo, reason}; undo is deleted | shut_down | left_running | already_gone."),
			mcpgo.WithString("lease_id", mcpgo.Description("Lease id from sim_acquire (or pass udid)")),
			mcpgo.WithString("udid", mcpgo.Description("Leased simulator UDID (alternative to lease_id)")),
			mcpgo.WithString("owner", mcpgo.Required(), mcpgo.Description("The lease's owner")),
		),
		mcpgo.NewTool("sim_renew",
			mcpgo.WithDescription("Extend a simulator lease. Any spyder verb that uses the leased UDID also renews it; call this when you hold a simulator idle."),
			mcpgo.WithString("lease_id", mcpgo.Description("Lease id (or pass udid)")),
			mcpgo.WithString("udid", mcpgo.Description("Leased simulator UDID")),
			mcpgo.WithString("owner", mcpgo.Required(), mcpgo.Description("The lease's owner")),
			mcpgo.WithNumber("ttl_s", mcpgo.Description("New lease length in seconds; omit to keep the current one")),
		),
		mcpgo.NewTool("sim_inventory",
			mcpgo.WithDescription("Every iOS simulator and Android AVD, and every runtime and system image, with owner, size and last use (🎯T189). Read-only. Returns {schema_version: 1, generated_at, devices: [{kind: ios_simulator|android_avd, id, name, device_type, runtime, state, path, bytes, owner: {kind: lease|pool|claim|none, owner, project, lease_id, expires_at, note}, created_by_spyder, created_for, last_leased_by, last_used_at, last_used_source: spyder|simctl|mtime}], runtimes: [{kind: ios_runtime|android_system_image, id, image_id, version, path, bytes, deletable, used_by: {count, ids}, keep}], errors}. `keep: \"newest\"` marks the newest image of a platform, which sim_prune_runtimes spares."),
		),
		mcpgo.NewTool("sim_prune",
			mcpgo.WithDescription("List (dry_run, the default) or delete simulators and AVDs that nobody owns — no lease, claim or pool — that are shut down and idle longer than idle_days. Never touches a booted, leased, claimed or pool device. Deleting needs dry_run=false and owner; each deletion is logged to ~/.spyder/sim-prune.jsonl. Returns {dry_run, candidates: [{kind, id, name, bytes, last_used_at, reason}], bytes, deleted, errors}."),
			mcpgo.WithBoolean("unowned", mcpgo.Description("Must be true (the default): only unowned devices are ever pruned")),
			mcpgo.WithNumber("idle_days", mcpgo.Description("Idle threshold in days (default 7)")),
			mcpgo.WithBoolean("dry_run", mcpgo.Description("List only (default true). Pass false, with owner, to delete.")),
			mcpgo.WithString("owner", mcpgo.Description("Who is pruning; required to delete")),
		),
		mcpgo.NewTool("sim_prune_runtimes",
			mcpgo.WithDescription("List (dry_run, the default) or delete iOS runtimes and Android system images that no simulator or AVD uses. The newest image of each platform is kept unless include_newest. Deleting needs dry_run=false and owner. Returns the same shape as sim_prune."),
			mcpgo.WithBoolean("dry_run", mcpgo.Description("List only (default true)")),
			mcpgo.WithString("owner", mcpgo.Description("Who is pruning; required to delete")),
			mcpgo.WithBoolean("include_newest", mcpgo.Description("Also prune the newest unused image of a platform")),
		),
		mcpgo.NewTool("sim_claim",
			mcpgo.WithDescription("Mark a simulator or AVD as yours (owner kind `claim`): sim_acquire will not reuse it for anyone else and sim_prune never offers it. For devices made by hand."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Simulator UDID or AVD name")),
			mcpgo.WithString("owner", mcpgo.Required(), mcpgo.Description("Who it belongs to")),
			mcpgo.WithString("project", mcpgo.Description("Optional project")),
			mcpgo.WithString("note", mcpgo.Description("Optional note")),
		),
		mcpgo.NewTool("sim_unclaim",
			mcpgo.WithDescription("Remove your claim on a simulator or AVD."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Simulator UDID or AVD name")),
			mcpgo.WithString("owner", mcpgo.Required(), mcpgo.Description("The claim's owner")),
		),
		mcpgo.NewTool("sim_leases",
			mcpgo.WithDescription("List simulator leases, and booted simulators spyder holds no lease for (`unleased_booted`, with uptime_s). Unleased simulators are reported only — spyder never shuts them down. Read-only."),
		),
	}
}
