// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package battery records connected-device charge over time (🎯T137).
// The daemon samples iOS/Android adapters on a one-minute interval and
// persists daily JSONL under ~/.spyder/battery/; the dashboard and the
// battery_history verb read that store.
package battery

import (
	"log/slog"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

const (
	// DefaultInterval is how often the daemon probes connected devices.
	DefaultInterval = time.Minute
	// DefaultRetention is how long daily JSONL files are kept.
	DefaultRetention = 14 * 24 * time.Hour
)

// Sample is one battery observation for one device.
type Sample struct {
	TS           time.Time `json:"ts"`
	DeviceID     string    `json:"device_id"`
	Alias        string    `json:"alias,omitempty"`
	Platform     string    `json:"platform,omitempty"`
	BatteryLevel *int      `json:"battery_level,omitempty"` // 0..100
	Charging     *bool     `json:"charging,omitempty"`
}

// Collect reads State for each connected mobile device and returns
// samples that have a battery_level. Desktop hosts are skipped. A
// State error or a missing battery_level skips that device for this
// tick; other devices are still collected.
func Collect(now time.Time, infos []device.Info, state func(id string) (device.State, error)) []Sample {
	if state == nil {
		return nil
	}
	now = now.UTC()
	out := make([]Sample, 0, len(infos))
	for _, info := range infos {
		if info.Platform == "desktop" || info.UUID == "" {
			continue
		}
		st, err := state(info.UUID)
		if err != nil {
			slog.Debug("battery: state failed", "device", info.UUID, "error", err)
			continue
		}
		if st.BatteryLevel == nil {
			continue
		}
		level := *st.BatteryLevel
		sm := Sample{
			TS:           now,
			DeviceID:     info.UUID,
			Alias:        info.Alias,
			Platform:     info.Platform,
			BatteryLevel: &level,
		}
		if sm.Alias == "" {
			sm.Alias = info.Name
		}
		if st.Charging != nil {
			c := *st.Charging
			sm.Charging = &c
		}
		out = append(out, sm)
	}
	return out
}

// Downsample keeps the last sample per (device, bucket). A non-positive
// bucket returns samples unchanged. Input order is preserved for first
// appearance of each bucket.
func Downsample(samples []Sample, bucket time.Duration) []Sample {
	if bucket <= 0 || len(samples) == 0 {
		return samples
	}
	sec := int64(bucket / time.Second)
	if sec <= 0 {
		return samples
	}
	type key struct {
		id string
		b  int64
	}
	last := make(map[key]Sample, len(samples))
	order := make([]key, 0, len(samples))
	for _, s := range samples {
		k := key{id: s.DeviceID, b: s.TS.UTC().Unix() / sec}
		if _, ok := last[k]; !ok {
			order = append(order, k)
		}
		last[k] = s
	}
	out := make([]Sample, 0, len(order))
	for _, k := range order {
		out = append(out, last[k])
	}
	return out
}

// Latest returns the most recent sample per device_id, in first-seen order.
func Latest(samples []Sample) []Sample {
	idx := make(map[string]int, len(samples))
	out := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if i, ok := idx[s.DeviceID]; ok {
			if !s.TS.Before(out[i].TS) {
				out[i] = s
			}
			continue
		}
		idx[s.DeviceID] = len(out)
		out = append(out, s)
	}
	return out
}
