// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T130 — allowlisted device settings (Android real; iOS fail-closed).

package mcp

import (
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
)

type systemSetter interface {
	SetSystemSetting(id, key, value string) (device.SettingResult, error)
	RestoreSystemSetting(id, key string) (device.SettingResult, error)
	GetSystemSetting(id, key string) (device.SettingResult, error)
}

func (h *Handler) handleDeviceSetting(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	key, err := requireString(args, "key")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	restore := false
	if v, ok := args["restore"].(bool); ok {
		restore = v
	}
	getOnly := false
	if v, ok := args["get"].(bool); ok {
		getOnly = v
	}
	value := optString(args, "value")

	h.mu.Lock()
	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
	h.mu.Unlock()
	if errRes != nil {
		return errRes, nil
	}
	setter, ok := adapter.(systemSetter)
	if !ok {
		return toolErr("adapter does not support device_setting")
	}

	var result device.SettingResult
	var serr error
	switch {
	case restore:
		result, serr = setter.RestoreSystemSetting(id, key)
	case value != "" && !getOnly:
		result, serr = setter.SetSystemSetting(id, key, value)
	default:
		result, serr = setter.GetSystemSetting(id, key)
	}
	if serr != nil {
		return toolErr("device_setting: %v", serr)
	}
	return toolJSON(map[string]any{
		"device":   dev,
		"platform": platform,
		"result":   result,
	})
}
