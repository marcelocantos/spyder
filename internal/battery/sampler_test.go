// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package battery

import (
	"context"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

func TestSampler_TickPersistsConnectedDevices(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	levelPad, levelPhone := 55, 12
	chg := true
	s := NewSampler(SamplerArgs{
		Store: st,
		Now:   func() time.Time { return now },
		List: func() ([]device.Info, error) {
			return []device.Info{
				{UUID: "pad", Platform: "ios", Alias: "iPad"},
				{UUID: "phone", Platform: "android", Alias: "S24"},
				{UUID: "mac", Platform: "desktop", Alias: "Mac"},
			}, nil
		},
		State: func(id string) (device.State, error) {
			switch id {
			case "pad":
				return device.State{BatteryLevel: &levelPad, Charging: &chg}, nil
			case "phone":
				return device.State{BatteryLevel: &levelPhone}, nil
			default:
				t.Fatalf("State(%q)", id)
				return device.State{}, nil
			}
		},
	})
	if err := s.Tick(); err != nil {
		t.Fatal(err)
	}
	got, err := st.Query(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 (desktop skipped)", len(got))
	}
}

func TestSampler_TickContinuesAfterOneFailure(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	level := 90
	s := NewSampler(SamplerArgs{
		Store: st,
		Now:   func() time.Time { return time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC) },
		List: func() ([]device.Info, error) {
			return []device.Info{
				{UUID: "bad", Platform: "ios"},
				{UUID: "good", Platform: "android", Alias: "ok"},
			}, nil
		},
		State: func(id string) (device.State, error) {
			if id == "bad" {
				return device.State{}, errBoom
			}
			return device.State{BatteryLevel: &level}, nil
		},
	})
	if err := s.Tick(); err != nil {
		t.Fatal(err)
	}
	got, err := st.Query(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != "good" {
		t.Fatalf("got %+v", got)
	}
}

func TestSampler_RunStopsOnCancel(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	level := 1
	s := NewSampler(SamplerArgs{
		Store:    st,
		Interval: time.Hour,
		List: func() ([]device.Info, error) {
			return []device.Info{{UUID: "a", Platform: "ios"}}, nil
		},
		State: func(string) (device.State, error) {
			return device.State{BatteryLevel: &level}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
