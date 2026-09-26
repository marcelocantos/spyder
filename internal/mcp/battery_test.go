// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/battery"
	"github.com/marcelocantos/spyder/internal/device"
)

func TestHandleBatteryHistory_EmptyStore(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "battery_history", map[string]any{})
	if r.IsError {
		t.Fatalf("empty store should succeed: %s", resultText(t, &r))
	}
	var body batteryHistoryResult
	if err := json.Unmarshal([]byte(resultText(t, &r)), &body); err != nil {
		t.Fatal(err)
	}
	if body.Samples == nil || body.Latest == nil {
		t.Fatalf("nil slices: %+v", body)
	}
	if len(body.Samples) != 0 {
		t.Errorf("samples = %d, want 0", len(body.Samples))
	}
}

func TestHandleBatteryHistory_ReadsStoreAndLatest(t *testing.T) {
	h := newTestHandler(t)
	st, err := battery.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.SetBatteryStore(st)
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	l1, l2 := 90, 40
	chg := true
	mustAppend(t, st, battery.Sample{TS: t0, DeviceID: "00008103-001122334455667A", Alias: "iPad", Platform: "ios", BatteryLevel: &l1, Charging: &chg})
	mustAppend(t, st, battery.Sample{TS: t0.Add(time.Hour), DeviceID: "00008103-001122334455667A", Alias: "iPad", Platform: "ios", BatteryLevel: &l2})
	mustAppend(t, st, battery.Sample{TS: t0, DeviceID: "R5CR112X76K", Alias: "Raspberry", Platform: "android", BatteryLevel: &l1})

	r := dispatchJSON(t, h, "battery_history", map[string]any{"since": "-6h"})
	if r.IsError {
		t.Fatalf("%s", resultText(t, &r))
	}
	var body batteryHistoryResult
	if err := json.Unmarshal([]byte(resultText(t, &r)), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Samples) != 3 {
		t.Fatalf("samples = %d, want 3: %+v", len(body.Samples), body.Samples)
	}
	if len(body.Latest) != 2 {
		t.Fatalf("latest = %d, want 2", len(body.Latest))
	}

	one := dispatchJSON(t, h, "battery_history", map[string]any{"device": "iPad", "since": "-6h"})
	if one.IsError {
		t.Fatalf("%s", resultText(t, &one))
	}
	var filtered batteryHistoryResult
	if err := json.Unmarshal([]byte(resultText(t, &one)), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered.Samples) != 2 {
		t.Fatalf("iPad samples = %d, want 2", len(filtered.Samples))
	}
	for _, sm := range filtered.Samples {
		if sm.Alias != "iPad" {
			t.Errorf("unexpected sample %+v", sm)
		}
	}
}

func TestHandleBatteryHistory_SampleTicksLiveDevices(t *testing.T) {
	level := 73
	charging := true
	ios := &stubAdapter{
		list: func() ([]device.Info, error) {
			return []device.Info{{UUID: "00008103-001122334455667A", Platform: "ios"}}, nil
		},
		state: func(id string) (device.State, error) {
			return device.State{BatteryLevel: &level, Charging: &charging}, nil
		},
	}
	h := newHandlerWithStubs(t, ios, &stubAdapter{})
	st, err := battery.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.SetBatteryStore(st)

	r := dispatchJSON(t, h, "battery_history", map[string]any{"sample": true, "since": "-1h"})
	if r.IsError {
		t.Fatalf("%s", resultText(t, &r))
	}
	var body batteryHistoryResult
	if err := json.Unmarshal([]byte(resultText(t, &r)), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Samples) != 1 {
		t.Fatalf("after sample ticks, samples = %d, want 1: %+v", len(body.Samples), body.Samples)
	}
	if body.Samples[0].Alias != "iPad" {
		t.Errorf("alias = %q, want iPad (inventory)", body.Samples[0].Alias)
	}
	if body.Samples[0].BatteryLevel == nil || *body.Samples[0].BatteryLevel != 73 {
		t.Errorf("level = %v", body.Samples[0].BatteryLevel)
	}
}

func TestHandleBatteryHistory_BucketDownsamples(t *testing.T) {
	h := newTestHandler(t)
	st, err := battery.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.SetBatteryStore(st)
	t0 := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Minute)
	for i := range 10 {
		l := 50 + i
		mustAppend(t, st, battery.Sample{
			TS:           t0.Add(time.Duration(i) * time.Minute),
			DeviceID:     "00008103-001122334455667A",
			Alias:        "iPad",
			BatteryLevel: &l,
		})
	}
	r := dispatchJSON(t, h, "battery_history", map[string]any{"since": "-1h", "bucket_s": 300})
	if r.IsError {
		t.Fatalf("%s", resultText(t, &r))
	}
	var body batteryHistoryResult
	if err := json.Unmarshal([]byte(resultText(t, &r)), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Samples) >= 10 {
		t.Fatalf("bucket_s=300 should downsample 10 minute samples, got %d", len(body.Samples))
	}
	if len(body.Samples) < 2 {
		t.Fatalf("over-downsampled: %d", len(body.Samples))
	}
}

func TestHandleBatteryHistory_BadSince(t *testing.T) {
	h := newTestHandler(t)
	r := dispatchJSON(t, h, "battery_history", map[string]any{"since": "not-a-time"})
	if !r.IsError {
		t.Fatal("expected error")
	}
}

func mustAppend(t *testing.T, st *battery.Store, sm battery.Sample) {
	t.Helper()
	if err := st.Append(sm); err != nil {
		t.Fatal(err)
	}
}
