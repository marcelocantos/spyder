// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// BindIOSRunwda arms ios runwda reinstantiation on the handler's iOS
// adapter (🎯T167). Stub adapters used by unit tests are left alone.
func (h *Handler) BindIOSRunwda(resv *reservations.Store, iosBin string) {
	if h == nil {
		return
	}
	ios, ok := h.ios.(*device.IOSAdapter)
	if !ok || ios == nil {
		return
	}
	ios.BindRunwda(&device.RunwdaBind{
		IOSBinary: iosBin,
		Reserved: func(udid string) bool {
			if resv == nil || udid == "" {
				return false
			}
			return resv.Authorize(udid, "") != nil
		},
	})
}
