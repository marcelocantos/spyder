// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package battery

import (
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

func TestCollect_SkipsDesktopAndNilBattery(t *testing.T) {
	level := 64
	charging := true
	infos := []device.Info{
		{UUID: "pad", Platform: "ios", Alias: "iPad", Name: "iPad"},
		{UUID: "desk", Platform: "desktop", Alias: "Mac"},
		{UUID: "empty", Platform: "android", Alias: "S24"},
		{UUID: "dead", Platform: "ios", Alias: "dead"},
	}
	got := Collect(time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), infos, func(id string) (device.State, error) {
		switch id {
		case "pad":
			return device.State{BatteryLevel: &level, Charging: &charging}, nil
		case "empty":
			return device.State{}, nil
		case "dead":
			return device.State{}, errBoom
		default:
			t.Fatalf("unexpected State(%q)", id)
			return device.State{}, nil
		}
	})
	if len(got) != 1 {
		t.Fatalf("Collect returned %d samples, want 1: %+v", len(got), got)
	}
	if got[0].DeviceID != "pad" || got[0].Alias != "iPad" {
		t.Errorf("sample = %+v", got[0])
	}
	if got[0].BatteryLevel == nil || *got[0].BatteryLevel != 64 {
		t.Errorf("level = %v", got[0].BatteryLevel)
	}
	if got[0].Charging == nil || !*got[0].Charging {
		t.Errorf("charging = %v", got[0].Charging)
	}
}

func TestCollect_ClonesBatteryDetails(t *testing.T) {
	level := 40
	charging := true
	st := device.State{
		BatteryLevel: &level,
		Charging:     &charging,
		Battery:      map[string]any{"current now": 203, "USB powered": true},
	}
	got := Collect(time.Now(), []device.Info{{UUID: "a", Platform: "android"}}, func(string) (device.State, error) {
		return st, nil
	})
	if len(got) != 1 || got[0].Details["current now"] != 203 {
		t.Fatalf("details = %+v", got[0].Details)
	}
	st.Battery["current now"] = 999
	if got[0].Details["current now"] != 203 {
		t.Error("Collect aliased Battery details map")
	}
}

func TestCollect_CopiesPointers(t *testing.T) {
	level := 10
	infos := []device.Info{{UUID: "a", Platform: "ios"}}
	got := Collect(time.Now(), infos, func(string) (device.State, error) {
		return device.State{BatteryLevel: &level}, nil
	})
	level = 99
	if *got[0].BatteryLevel != 10 {
		t.Errorf("Collect aliased BatteryLevel pointer; got %d", *got[0].BatteryLevel)
	}
}

func TestCollect_UsesNameWhenAliasEmpty(t *testing.T) {
	level := 50
	infos := []device.Info{{UUID: "u", Platform: "android", Name: "Pixel"}}
	got := Collect(time.Now(), infos, func(string) (device.State, error) {
		return device.State{BatteryLevel: &level}, nil
	})
	if got[0].Alias != "Pixel" {
		t.Errorf("alias = %q, want Pixel", got[0].Alias)
	}
}

func TestDownsample_KeepsLastInBucket(t *testing.T) {
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	l1, l2, l3 := 10, 20, 30
	in := []Sample{
		{TS: t0, DeviceID: "a", BatteryLevel: &l1},
		{TS: t0.Add(2 * time.Minute), DeviceID: "a", BatteryLevel: &l2},
		{TS: t0.Add(6 * time.Minute), DeviceID: "a", BatteryLevel: &l3},
		{TS: t0, DeviceID: "b", BatteryLevel: &l1},
	}
	got := Downsample(in, 5*time.Minute)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if *got[0].BatteryLevel != 20 {
		t.Errorf("first bucket last = %d, want 20", *got[0].BatteryLevel)
	}
	if got[1].DeviceID != "a" || *got[1].BatteryLevel != 30 {
		t.Errorf("second a bucket = %+v", got[1])
	}
	if got[2].DeviceID != "b" {
		t.Errorf("b dropped: %+v", got)
	}
}

func TestDownsample_ZeroBucketIsIdentity(t *testing.T) {
	in := []Sample{{DeviceID: "a"}}
	if got := Downsample(in, 0); len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestLatest_PicksNewestPerDevice(t *testing.T) {
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	l1, l2, l3 := 1, 2, 3
	in := []Sample{
		{TS: t0, DeviceID: "a", BatteryLevel: &l1},
		{TS: t0.Add(time.Minute), DeviceID: "b", BatteryLevel: &l2},
		{TS: t0.Add(2 * time.Minute), DeviceID: "a", BatteryLevel: &l3},
	}
	got := Latest(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].DeviceID != "a" || *got[0].BatteryLevel != 3 {
		t.Errorf("a = %+v", got[0])
	}
	if got[1].DeviceID != "b" {
		t.Errorf("b = %+v", got[1])
	}
}

type boomError struct{}

func (boomError) Error() string { return "boom" }

var errBoom boomError
