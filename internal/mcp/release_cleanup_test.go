// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/reservations"
)

func releaseTestCall(t *testing.T, h *Handler, name string, args map[string]any) string {
	t.Helper()
	res, err := h.Dispatch(context.Background(), name, args)
	if err != nil || res == nil || res.IsError {
		t.Fatalf("%s: %v %+v", name, err, res)
	}
	return resultErrorText(t, res)
}

// Release stops the apps spyder launched for the holder. It used to do
// that after freeing the reservation, so the next holder's launch of
// the same app could be killed by it (DH-28). The stub's slow terminate
// stands for the kill request in flight to the device.
func TestReleaseStopsAppsBeforeTheNextHolderGetsTheDevice(t *testing.T) {
	var mu sync.Mutex
	running := map[string]bool{}
	ios := &stubAdapter{
		launchApp: func(_, bundle string, _ map[string]string) error {
			mu.Lock()
			running[bundle] = true
			mu.Unlock()
			return nil
		},
		terminateApp: func(_, bundle string) error {
			time.Sleep(100 * time.Millisecond)
			mu.Lock()
			running[bundle] = false
			mu.Unlock()
			return nil
		},
		appPID: func(_, bundle string) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			if running[bundle] {
				return 4242, nil
			}
			return 0, errors.New("app not running")
		},
	}
	h := newHandlerWithStubs(t, ios, &stubAdapter{})
	resv, err := reservations.New("")
	if err != nil {
		t.Fatal(err)
	}
	h.reservations = resv
	const game = "com.example.game"
	releaseTestCall(t, h, "reserve", map[string]any{"device": "iPad", "owner": "job-A"})
	releaseTestCall(t, h, "launch_app", map[string]any{"device": "iPad", "owner": "job-A", "bundle_id": game})
	text := releaseTestCall(t, h, "release", map[string]any{"device": "iPad", "owner": "job-A"})
	mu.Lock()
	stillRunning := running[game]
	mu.Unlock()
	if stillRunning {
		t.Errorf("release returned (%s) before job-A's app was stopped", text)
	}
	// job-B was waiting for the device and starts at once.
	releaseTestCall(t, h, "reserve", map[string]any{"device": "iPad", "owner": "job-B"})
	releaseTestCall(t, h, "launch_app", map[string]any{"device": "iPad", "owner": "job-B", "bundle_id": game})
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !running[game] {
		t.Errorf("job-B holds iPad and launched %s, but job-A's release stopped it", game)
	}
}

// A caller who does not hold the device is refused, and its release
// must not stop the holder's apps on the way.
func TestReleaseByNonHolderStopsNothing(t *testing.T) {
	var mu sync.Mutex
	terminated := 0
	ios := &stubAdapter{
		launchApp:    func(string, string, map[string]string) error { return nil },
		terminateApp: func(string, string) error { mu.Lock(); terminated++; mu.Unlock(); return nil },
	}
	h := newHandlerWithStubs(t, ios, &stubAdapter{})
	resv, err := reservations.New("")
	if err != nil {
		t.Fatal(err)
	}
	h.reservations = resv
	releaseTestCall(t, h, "reserve", map[string]any{"device": "iPad", "owner": "job-A"})
	releaseTestCall(t, h, "launch_app", map[string]any{"device": "iPad", "owner": "job-A", "bundle_id": "com.example.game"})
	res, err := h.Dispatch(context.Background(), "release", map[string]any{"device": "iPad", "owner": "job-B"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("job-B released job-A's device: %+v", res)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if terminated != 0 {
		t.Fatalf("job-B's refused release stopped %d of job-A's apps", terminated)
	}
}
