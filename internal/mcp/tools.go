// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/marcelocantos/spyder/internal/device"
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

func (h *Handler) handleDevices(args map[string]any) (string, bool, error) {
	platform := optString(args, "platform")
	if platform == "" {
		platform = "all"
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var devices []device.Info
	if platform == "ios" || platform == "all" {
		ds, err := h.ios.List()
		if err != nil {
			return fmt.Sprintf("ios: %v", err), true, nil
		}
		devices = append(devices, ds...)
	}
	if platform == "android" || platform == "all" {
		ds, err := h.android.List()
		if err != nil && platform == "android" {
			return fmt.Sprintf("android: %v", err), true, nil
		}
		devices = append(devices, ds...)
	}

	for i := range devices {
		if alias := h.inventory.AliasFor(devices[i].UUID); alias != "" {
			devices[i].Alias = alias
		}
	}

	return marshal(devices)
}

func (h *Handler) handleResolve(args map[string]any) (string, bool, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return "", false, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	entry, ok := h.inventory.Lookup(name)
	if !ok {
		return fmt.Sprintf("unknown device %q (check inventory at %s)", name, h.inventory.Path()), true, nil
	}

	return marshal(entry)
}

func (h *Handler) handleKeepAwake(args map[string]any) (string, bool, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return "", false, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, id, err := h.resolveAdapter(dev)
	if err != nil {
		return err.Error(), true, nil
	}
	if err := adapter.LaunchKeepAwake(id); err != nil {
		return fmt.Sprintf("launching KeepAwake on %s: %v", dev, err), true, nil
	}
	return fmt.Sprintf("KeepAwake launched on %s", dev), false, nil
}

func (h *Handler) handleDeviceState(args map[string]any) (string, bool, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return "", false, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, id, err := h.resolveAdapter(dev)
	if err != nil {
		return err.Error(), true, nil
	}
	state, err := adapter.State(id)
	if err != nil {
		return fmt.Sprintf("reading state: %v", err), true, nil
	}
	return marshal(state)
}

// resolveAdapter maps a user-provided device reference (alias or raw UUID)
// to the platform adapter and the platform-specific identifier it expects.
// Raw UUIDs that aren't in the inventory default to the iOS adapter.
func (h *Handler) resolveAdapter(ref string) (device.Adapter, string, error) {
	entry, ok := h.inventory.Lookup(ref)
	if !ok {
		return h.ios, ref, nil
	}
	switch entry.Platform {
	case "ios":
		id := entry.IOSUUID
		if id == "" {
			id = entry.IOSCoreDevice
		}
		return h.ios, id, nil
	case "android":
		return h.android, entry.AndroidSerial, nil
	default:
		return nil, "", fmt.Errorf("inventory entry %q has unknown platform %q", ref, entry.Platform)
	}
}

func marshal(v any) (string, bool, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", false, err
	}
	return string(out), false, nil
}
