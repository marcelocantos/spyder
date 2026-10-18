// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"log/slog"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/agent"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// AttachIOSAgent binds the shipped XCTest supervisor to the handler's
// iOS adapter. A stub adapter (unit tests, daemon.Build without the
// serve opt-in) is left alone so nothing launches a runner.
func (h *Handler) AttachIOSAgent(resv *reservations.Store) {
	ios, ok := h.ios.(*device.IOSAdapter)
	if !ok || ios == nil {
		return
	}
	host := device.NewAutomationHost(ios)
	sup := agent.NewSupervisor(&agent.SupervisorArgs{
		Effects: host.Effects(),
		Probe:   host.Probe,
		OwnerOf: func(id string) (string, bool) {
			if resv == nil {
				return "", false
			}
			r, ok := resv.Get(id)
			if !ok || r.Owner == "" {
				return "", false
			}
			return r.Owner, true
		},
	})
	ios.BindSupervisor(sup)
	h.mu.Lock()
	h.agents = sup
	h.agentHost = host
	h.mu.Unlock()
}

func (h *Handler) noteLaunchedAUT(id, bundle string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	host := h.agentHost
	h.mu.Unlock()
	if host != nil {
		host.NoteLaunched(id, bundle)
	}
}

// AgentSupervisor is the shipped per-device supervisor, or nil when
// this process did not opt into XCTest supervision.
func (h *Handler) AgentSupervisor() *agent.Supervisor {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents
}

// TickAgents is one supervision pass over currently listed iOS devices.
// It is not called from unit-test daemons.
func (h *Handler) TickAgents() {
	if h == nil {
		return
	}
	h.mu.Lock()
	sup := h.agents
	ios := h.ios
	h.mu.Unlock()
	if sup == nil || ios == nil {
		return
	}
	list, err := ios.List()
	if err != nil {
		slog.Warn("agent tick list", "error", err)
		return
	}
	for _, info := range list {
		if info.Platform != "" && info.Platform != "ios" {
			continue
		}
		if info.UUID == "" {
			continue
		}
		if _, err := sup.Tick(info.UUID); err != nil {
			slog.Warn("agent tick", "device", info.UUID, "error", err)
		}
	}
}

// StopAgents ends resident XCTest sessions. Idle does not.
func (h *Handler) StopAgents() {
	if h == nil {
		return
	}
	h.mu.Lock()
	host := h.agentHost
	h.mu.Unlock()
	if host != nil {
		host.Shutdown()
	}
}

// gateAgent rejects a mutation when a supervised client already owns
// the device and the caller is not that owner. Before any client has
// called agent_ensure, the existing reservation check stands.
func (h *Handler) gateAgent(deviceID, owner string, kind agent.Mutation) *mcpgo.CallToolResult {
	sup := h.agents
	if sup == nil || deviceID == "" || !sup.HasOwner(deviceID) {
		return nil
	}
	if err := sup.Mutate(deviceID, owner, kind); err != nil {
		res, _ := toolErr("%v", err)
		return res
	}
	return nil
}

func (h *Handler) gateAgentAny(owner string, kind agent.Mutation, ids ...string) *mcpgo.CallToolResult {
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if res := h.gateAgent(id, owner, kind); res != nil {
			return res
		}
	}
	return nil
}

func (h *Handler) agentDeviceKey(dev string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _, id, err := h.resolveAdapter(dev)
	if err != nil || id == "" {
		return dev
	}
	return id
}

func (h *Handler) handleAgentEnsure(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	if h.agents == nil {
		return toolErr("agent supervision is not enabled")
	}
	snap, err := h.agents.Ensure(h.agentDeviceKey(dev), owner)
	if err != nil {
		return toolErr("agent_ensure on %s: %v", dev, err)
	}
	return toolJSON(snap)
}

func (h *Handler) handleAgentIdle(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	if h.agents == nil {
		return toolErr("agent supervision is not enabled")
	}
	snap, err := h.agents.Idle(h.agentDeviceKey(dev), owner)
	if err != nil {
		return toolErr("agent_idle on %s: %v", dev, err)
	}
	return toolJSON(snap)
}

func (h *Handler) handleAgentStatus(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	if h.agents == nil {
		return toolErr("agent supervision is not enabled")
	}
	key := h.agentDeviceKey(dev)
	snap, ok := h.agents.Snapshot(key)
	if !ok {
		snap, ok = h.agents.Snapshot(dev)
	}
	if !ok {
		return toolJSON(agent.Snapshot{DeviceID: key, Condition: agent.CondStartup})
	}
	return toolJSON(snap)
}

func agentDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("agent_ensure",
			mcpgo.WithDescription("Admit the reservation owner onto the supervised XCTest automation agent for one tethered iOS device (🎯T163). A healthy authorized runner is reused and not reinstalled. Consent required, lock, disconnect, and a dead runner past the retry cap are not reported ready."),
			mcpgo.WithString("device", mcpgo.Required(), mcpgo.Description("Device alias or UDID")),
			mcpgo.WithString("owner", mcpgo.Required(), mcpgo.Description("Reservation owner")),
		),
		mcpgo.NewTool("agent_idle",
			mcpgo.WithDescription("Stop the app under test and drop the build/Unity lease and any host xcodebuild or client testmanagerd session (🎯T163). A healthy runner may stay up. Requires the per-device owner."),
			mcpgo.WithString("device", mcpgo.Required(), mcpgo.Description("Device alias or UDID")),
			mcpgo.WithString("owner", mcpgo.Required(), mcpgo.Description("Reservation owner")),
		),
		mcpgo.NewTool("agent_status",
			mcpgo.WithDescription("Read the bounded XCTest agent condition for one device (🎯T163). Read-only. Ready is false while consent is required or the runner has failed."),
			mcpgo.WithString("device", mcpgo.Required(), mcpgo.Description("Device alias or UDID")),
		),
	}
}
