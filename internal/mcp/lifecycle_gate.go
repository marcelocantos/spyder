// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"sync"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type lifecycleGate struct {
	permit     chan struct{}
	references int // holder + waiters; guarded by lifecycleMu
}

func lifecycleTool(name string) bool {
	switch name {
	case "install_app", "uninstall_app", "deploy_app", "launch_app", "terminate_app", "ensure_session", "launch_player", "app_spawn",
		"is_running", "list_apps", "device_state", "screenshot":
		return true
	default:
		return false
	}
}

// withLifecycle preserves complete same-device pipelines without holding the
// global handler state lock across provider work. It is shared by REST/MCP and
// Starlark. Status reads also wait, so their timeout cannot invalidate a
// legitimate install using the same provider channel. Cancellation while
// queued must never become a late device mutation.
func (h *Handler) withLifecycle(ctx context.Context, name string, args map[string]any, fn toolFunc) (*mcpgo.CallToolResult, error) {
	ref, _ := args["device"].(string)
	if !lifecycleTool(name) || ref == "" {
		return fn(args)
	}
	h.mu.Lock()
	_, platform, id, err := h.resolveAdapter(ref)
	h.mu.Unlock()
	if err != nil {
		return toolErr("%v", err)
	}
	key := platform + "\x00" + id
	h.lifecycleMu.Lock()
	if h.lifecycleGates == nil {
		h.lifecycleGates = make(map[string]*lifecycleGate)
	}
	gate := h.lifecycleGates[key]
	if gate == nil {
		gate = &lifecycleGate{permit: make(chan struct{}, 1)}
		gate.permit <- struct{}{}
		h.lifecycleGates[key] = gate
	}
	gate.references++
	h.lifecycleMu.Unlock()
	acquired := false
	var once sync.Once
	release := func() {
		once.Do(func() {
			if acquired {
				gate.permit <- struct{}{}
			}
			h.lifecycleMu.Lock()
			gate.references--
			if gate.references == 0 {
				delete(h.lifecycleGates, key)
			}
			h.lifecycleMu.Unlock()
		})
	}
	defer release()
	select {
	case <-ctx.Done():
		return toolErr("lifecycle cancelled waiting for %s on %s: %v", name, ref, ctx.Err())
	case <-gate.permit:
		acquired = true
	}
	if err := ctx.Err(); err != nil {
		return toolErr("lifecycle cancelled before %s on %s: %v", name, ref, err)
	}
	return fn(args)
}
