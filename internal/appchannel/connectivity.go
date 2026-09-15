// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"strings"
	"time"
)

// Connectivity tunables. Vars so tests can shorten them.
var (
	ConnectivityPingSilence     = 90 * time.Second
	ConnectivityMissingDialBack = 2 * time.Minute
	ConnectivityLaunchWindow    = 15 * time.Minute
	ConnectivityPingTimeout     = 2 * time.Second
)

// ConnectivityReport counts app-channel paths that look unreachable from
// the device side — the signal the host Wi-Fi monitor uses instead of
// OS-level self-dials (which can succeed while inbound from devices fails).
type ConnectivityReport struct {
	MissingDialBacks int // listener up, app launched, no session yet
	PingFailures     int // live session failed a proactive ping
}

// StressPresent reports whether any device appears unable to reach spyder
// over the app-channel dial-back path.
func (r ConnectivityReport) StressPresent() bool {
	return r.MissingDialBacks > 0 || r.PingFailures > 0
}

// ProbeConnectivity inspects keyed listeners and live sessions. When a
// session has been quiet longer than ConnectivityPingSilence, spyder sends
// a proactive ping — an end-to-end check of the device→host path.
func ProbeConnectivity(m *Manager, launches map[AppKey]time.Time, now time.Time) ConnectivityReport {
	if m == nil {
		return ConnectivityReport{}
	}
	var out ConnectivityReport
	for _, l := range m.KeyedListeners() {
		if skipDevice(l.Key.DeviceID) {
			continue
		}
		sessions := l.Sessions()
		if len(sessions) == 0 {
			launched, ok := launches[l.Key]
			if !ok || now.Sub(launched) > ConnectivityLaunchWindow {
				continue
			}
			if now.Sub(l.LastTouched()) >= ConnectivityMissingDialBack {
				out.MissingDialBacks++
			}
			continue
		}
		for _, s := range sessions {
			if now.Sub(s.LastProgressAt()) < ConnectivityPingSilence {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), ConnectivityPingTimeout)
			_, err := s.Call(ctx, MethodPing, nil, ConnectivityPingTimeout)
			cancel()
			if err != nil {
				out.PingFailures++
			}
		}
	}
	return out
}

func skipDevice(deviceID string) bool {
	if deviceID == "" {
		return true
	}
	if strings.HasPrefix(deviceID, "emulator-") {
		return true
	}
	// Simulator IDs are UUID-shaped; physical iOS UDIDs are also UUIDs.
	// Callers on simulators use 127.0.0.1 — not a Wi-Fi path. Heuristic:
	// CoreSimulator devices often appear in devicectl with specific patterns;
	// keyed listeners for simulators are rare in production. Skip loopback-only
	// device ids used in tests.
	if deviceID == "127.0.0.1" || deviceID == "booted" {
		return true
	}
	return false
}
