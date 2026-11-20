// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/recording"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// Launches are recorded under resolveAdapter's id, and activity under
// deviceIDForRef's. For an iOS entry with only ios_coredevice the two
// differed, so a reserved device never looked active and the idle sweep
// stopped the holder's app mid-run (DH-29).
func TestIdleSweepKeepsAppOnReservedDevice(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
	}{
		{"ios_uuid", `{"alias":"iPhone","platform":"ios","ios_uuid":"00008110-000A1B2C3D4E5F60"}`},
		{"ios_coredevice only", `{"alias":"iPhone","platform":"ios","ios_coredevice":"11111111-2222-3333-4444-555555555555"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(paths.DataDirEnv, t.TempDir())
			t.Setenv("HOME", t.TempDir())
			if err := os.WriteFile(paths.InventoryPath(), []byte("["+tc.entry+"]"), 0o600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			terminated := 0
			ios := &stubAdapter{
				terminateApp: func(string, string) error { mu.Lock(); terminated++; mu.Unlock(); return nil },
				appPID:       func(string, string) (int, error) { return 0, errors.New("app not running") },
			}
			h := NewHandlerWithAdapters(ios, &stubAdapter{})
			h.recordings = recording.NewRegistry()
			h.networkByDevice = map[string]appliedNetwork{}
			resv, err := reservations.New("", reservations.WithNormalizer(h.ReservationKey))
			if err != nil {
				t.Fatal(err)
			}
			h.reservations = resv
			for _, c := range []struct {
				name string
				args map[string]any
			}{
				{"reserve", map[string]any{"device": "iPhone", "owner": "job-A"}},
				{"launch_app", map[string]any{"device": "iPhone", "owner": "job-A", "bundle_id": "com.example.game"}},
			} {
				if res, err := h.Dispatch(context.Background(), c.name, c.args); err != nil || res.IsError {
					t.Fatalf("%s: %v %+v", c.name, err, res)
				}
			}
			// The battery tick after the idle limit; job-A still holds the device.
			h.sweepIdle(time.Now().Add(idleStopAfter + time.Minute))
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			if terminated > 0 {
				t.Errorf("idle sweep stopped job-A's app although job-A holds iPhone")
			}
		})
	}
}
