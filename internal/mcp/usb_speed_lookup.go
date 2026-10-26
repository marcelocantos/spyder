// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"

	"github.com/marcelocantos/spyder/internal/device"
)

func (h *Handler) cachedUSBSpeedLabel(devRef string) string {
	if h == nil || devRef == "" {
		return ""
	}
	h.mu.Lock()
	alias := devRef
	if h.inventory != nil {
		if e, ok := h.inventory.Lookup(devRef); ok && e.Alias != "" {
			alias = e.Alias
		}
	}
	h.mu.Unlock()

	h.devicesMu.Lock()
	devices := append([]device.Info(nil), h.lastIOS...)
	h.devicesMu.Unlock()

	for _, d := range devices {
		if d.USBSpeed == "" {
			continue
		}
		if strings.EqualFold(d.UUID, devRef) || strings.EqualFold(d.Alias, devRef) ||
			strings.EqualFold(d.Name, devRef) || strings.EqualFold(d.Alias, alias) ||
			strings.EqualFold(d.Name, alias) {
			return d.USBSpeed
		}
	}
	return ""
}
