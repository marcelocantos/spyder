// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package usbspeed

import (
	"net"
	"strings"

	"github.com/marcelocantos/spyder/internal/device"
)

// Enrich is the shipped census/join/ceiling pipeline. It parses canned
// or live ioreg bytes, writes usb_speed onto matching USB-attached
// physical phones/tablets, and ratchets the ceiling store. Wireless
// ADB, iOS simulators, Android emulators, and desktop entries are left
// untouched. ioreg-shaped garbage or a missing serial omits the USB
// fields; the rest of the slice is unchanged.
//
// seeds maps a device uuid/serial to an optional inventory usb_max
// label used only when the store has no ceiling yet.
func Enrich(devices []device.Info, census []byte, store *Store, seeds map[string]string) {
	parsed := ParseCensus(census)
	dirty := false
	for i := range devices {
		if skipUSBMeta(devices[i]) {
			continue
		}
		live, ok := SpeedForID(devices[i].UUID, parsed)
		if !ok {
			continue
		}
		label, ok := Label(live)
		if !ok {
			continue
		}
		devices[i].USBSpeed = label

		if store == nil {
			continue
		}
		id := devices[i].UUID
		if !store.Has(id) {
			if seed, ok := seedSpeed(id, seeds); ok {
				store.SetCeiling(id, seed)
				dirty = true
			}
		}
		ceiling, anomaly, changed := store.Observe(id, live)
		if changed {
			dirty = true
		}
		if ceilLabel, ok := Label(ceiling); ok {
			devices[i].USBCeiling = ceilLabel
		}
		if anomaly {
			devices[i].USBAnomaly = true
		}
	}
	if dirty {
		_ = store.Save()
	}
}

func skipUSBMeta(d device.Info) bool {
	switch strings.ToLower(d.Platform) {
	case "desktop":
		return true
	case "ios":
		return device.IsSimulatorID(d.UUID)
	case "android":
		if strings.HasPrefix(d.UUID, "emulator-") {
			return true
		}
		if _, _, err := net.SplitHostPort(d.UUID); err == nil {
			return true
		}
	}
	return d.UUID == ""
}

func seedSpeed(id string, seeds map[string]string) (int, bool) {
	if len(seeds) == 0 {
		return 0, false
	}
	want := CanonicalID(id)
	for k, label := range seeds {
		if CanonicalID(k) != want {
			continue
		}
		return ParseLabel(label)
	}
	return 0, false
}
