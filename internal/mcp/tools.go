// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
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
	var perAdapterErrors []string

	if platform == "ios" || platform == "all" {
		ds, err := h.ios.List()
		if err != nil {
			if platform == "ios" {
				return fmt.Sprintf("ios: %v", err), true, nil
			}
			perAdapterErrors = append(perAdapterErrors, fmt.Sprintf("ios: %v", err))
		}
		devices = append(devices, ds...)
	}
	if platform == "android" || platform == "all" {
		ds, err := h.android.List()
		if err != nil {
			if platform == "android" {
				return fmt.Sprintf("android: %v", err), true, nil
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

	// When listing "all", surface per-adapter errors as a wrapped shape
	// so partial results aren't lost.
	if platform == "all" && len(perAdapterErrors) > 0 {
		return marshal(struct {
			Devices []device.Info `json:"devices"`
			Errors  []string      `json:"errors,omitempty"`
		}{devices, perAdapterErrors})
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
		entry = inventory.ClassifyRaw(name)
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

	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return err.Error(), true, nil
	}
	if err := adapter.LaunchKeepAwake(id); err != nil {
		return fmt.Sprintf("launching KeepAwake on %s: %v", dev, err), true, nil
	}
	switch platform {
	case "android":
		return fmt.Sprintf("KeepAwake is a no-op on %s: Android handles stay-awake natively — enable Settings → Developer options → Stay awake while plugged in", dev), false, nil
	default:
		return fmt.Sprintf("KeepAwake launched on %s", dev), false, nil
	}
}

func (h *Handler) handleDeviceState(args map[string]any) (string, bool, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return "", false, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, _, id, err := h.resolveAdapter(dev)
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

func marshal(v any) (string, bool, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", false, err
	}
	return string(out), false, nil
}
