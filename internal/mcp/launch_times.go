// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

// LaunchTimesSnapshot returns a copy of recent launch_app / ensure_session
// timestamps keyed by (device_id, bundle_id). Used by the host Wi-Fi monitor
// to detect apps that were launched but never completed the dial-back.
func (h *Handler) LaunchTimesSnapshot() map[appchannel.AppKey]time.Time {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.launchTimes) == 0 {
		return nil
	}
	out := make(map[appchannel.AppKey]time.Time, len(h.launchTimes))
	for k, t := range h.launchTimes {
		out[appchannel.AppKey{DeviceID: k.deviceID, BundleID: k.bundleID}] = t
	}
	return out
}
