// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"log/slog"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/battery"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/paths"
)

type batteryHistoryResult struct {
	Since   time.Time        `json:"since"`
	Until   time.Time        `json:"until"`
	BucketS int64            `json:"bucket_s,omitempty"`
	Samples []battery.Sample `json:"samples"`
	Latest  []battery.Sample `json:"latest"`
}

// StartBatterySampler opens ~/.spyder/battery/ if needed and runs the
// connected-device charge sampler until ctx is cancelled (🎯T137).
func (h *Handler) StartBatterySampler(ctx context.Context) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.batteryStore == nil {
		st, err := battery.Open(paths.BatteryDir())
		if err != nil {
			h.mu.Unlock()
			slog.Warn("battery: store unavailable; history disabled", "error", err)
			return
		}
		h.batteryStore = st
	}
	store := h.batteryStore
	h.mu.Unlock()

	slog.Info("battery: sampler started",
		"dir", store.Dir(), "interval", battery.DefaultInterval)
	battery.NewSampler(battery.SamplerArgs{
		Store: store,
		List:  h.listMobileDevices,
		State: h.stateForBattery,
	}).Run(ctx)
}

func (h *Handler) handleBatteryHistory(args map[string]any) (*mcpgo.CallToolResult, error) {
	sampleNow, _ := args["sample"].(bool)
	if sampleNow {
		h.sampleBatteriesOnce()
	}

	until := time.Now().UTC()
	since := until.Add(-24 * time.Hour)
	if v := optString(args, "since"); v != "" {
		t, err := parseTimeArg(v)
		if err != nil {
			return toolErr("since: %v", err)
		}
		since = t.UTC()
	}
	if v := optString(args, "until"); v != "" {
		t, err := parseTimeArg(v)
		if err != nil {
			return toolErr("until: %v", err)
		}
		until = t.UTC()
	}

	var matchID string
	if dev := optString(args, "device"); dev != "" {
		h.mu.Lock()
		_, _, id, err := h.resolveAdapter(dev)
		h.mu.Unlock()
		if err != nil {
			return toolErr("%v", err)
		}
		matchID = id
	}

	h.mu.Lock()
	store := h.batteryStore
	h.mu.Unlock()

	result := batteryHistoryResult{
		Since:   since,
		Until:   until,
		BucketS: optNumber(args, "bucket_s"),
		Samples: []battery.Sample{},
		Latest:  []battery.Sample{},
	}
	if store == nil {
		return toolJSON(result)
	}

	samples, err := store.Query(battery.Query{
		Since:    since,
		Until:    until,
		DeviceID: matchID,
	})
	if err != nil {
		return toolErr("battery history: %v", err)
	}
	// Alias match: the caller may have passed an alias that didn't
	// resolve onto the stored device_id (inventory miss). Also keep
	// samples whose alias equals the original device argument.
	if dev := optString(args, "device"); dev != "" && matchID != "" {
		filtered := samples[:0]
		for _, sm := range samples {
			if sm.DeviceID == matchID || sm.Alias == dev || sm.DeviceID == dev {
				filtered = append(filtered, sm)
			}
		}
		samples = filtered
	}
	result.Latest = battery.Latest(samples)
	if result.BucketS > 0 {
		samples = battery.Downsample(samples, time.Duration(result.BucketS)*time.Second)
	}
	if samples == nil {
		samples = []battery.Sample{}
	}
	if result.Latest == nil {
		result.Latest = []battery.Sample{}
	}
	result.Samples = samples
	return toolJSON(result)
}

func (h *Handler) sampleBatteriesOnce() {
	h.mu.Lock()
	store := h.batteryStore
	h.mu.Unlock()
	if store == nil {
		return
	}
	_ = battery.NewSampler(battery.SamplerArgs{
		Store: store,
		List:  h.listMobileDevices,
		State: h.stateForBattery,
	}).Tick()
}

func (h *Handler) listMobileDevices() ([]device.Info, error) {
	h.mu.Lock()
	ios, android, inv := h.ios, h.android, h.inventory
	h.mu.Unlock()

	var devices []device.Info
	if ios != nil {
		ds, err := ios.List()
		if err != nil {
			slog.Warn("battery: ios list failed", "error", err)
		} else {
			devices = append(devices, ds...)
		}
	}
	if android != nil {
		ds, err := android.List()
		if err != nil {
			slog.Warn("battery: android list failed", "error", err)
		} else {
			devices = append(devices, ds...)
		}
	}
	if inv != nil {
		for i := range devices {
			if a := inv.AliasFor(devices[i].UUID); a != "" {
				devices[i].Alias = a
			}
		}
	}
	return devices, nil
}

func (h *Handler) stateForBattery(id string) (device.State, error) {
	h.mu.Lock()
	adapter, _, resolved, err := h.resolveAdapter(id)
	h.mu.Unlock()
	if err != nil {
		return device.State{}, err
	}
	return adapter.State(resolved)
}
