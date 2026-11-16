// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/adbserver"
	"github.com/marcelocantos/spyder/internal/battery"
	"github.com/marcelocantos/spyder/internal/device"
)

// devices rows carry the sampler's fresh power reading and drop a stale one.
func TestAnnotatePower_FreshOnly(t *testing.T) {
	h := &Handler{lastPower: map[string]powerReading{
		"fresh": {power: device.Power{Health: device.PowerDraining}, at: time.Now()},
		"stale": {power: device.Power{Health: device.PowerOK}, at: time.Now().Add(-2 * powerFresh)},
	}}
	rows := []device.Info{{UUID: "fresh"}, {UUID: "stale"}, {UUID: "unseen"}}
	h.annotatePower(rows)
	if rows[0].Power == nil || rows[0].Power.Health != device.PowerDraining {
		t.Fatalf("fresh row: %+v", rows[0].Power)
	}
	if rows[1].Power != nil || rows[2].Power != nil {
		t.Fatalf("stale/unseen rows annotated: %+v %+v", rows[1].Power, rows[2].Power)
	}
}

// drainSamples feeds the policy one sample a minute ending at end.
func drainSamples(h *Handler, id string, end time.Time, charging bool, levels ...int) {
	for i, l := range levels {
		lvl, ch := l, charging
		ts := end.Add(time.Duration(i-len(levels)+1) * time.Minute)
		h.afterBatteryTick(ts, []battery.Sample{{TS: ts, DeviceID: id, BatteryLevel: &lvl, Charging: &ch}})
	}
}

// A device that falls to the floor is held: reserve and launches are
// refused until it is back to the resume level, force overrides, and the
// holder's own renewal is never refused (🎯T194.3, 🎯T194.4).
func TestPowerPolicy_HoldRefusesThenReleases(t *testing.T) {
	h, _ := newHandlerWithReservations(t, nil, nil)
	const id = "R58TESTDRAIN"
	now := time.Now()
	drainSamples(h, id, now, false, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9)
	if tr := h.trend(id); tr == nil || tr.State != device.DrainCritical {
		t.Fatalf("trend: %+v", tr)
	}

	r := dispatchJSON(t, h, "reserve", map[string]any{"device": id, "owner": "lab"})
	if !r.IsError || !strings.HasPrefix(resultText(t, &r), "needs_setup:") {
		t.Fatalf("reserve on a drained device: %s", resultText(t, &r))
	}
	if res := h.beforeVerb("launch_app", map[string]any{"device": id, "bundle_id": "com.x"}); res == nil {
		t.Fatal("launch_app on a held device was not refused")
	}
	if res := h.beforeVerb("screenshot", map[string]any{"device": id}); res != nil {
		t.Fatal("non-launch verbs are not held")
	}
	if res := h.beforeVerb("launch_app", map[string]any{"device": id, "force": true}); res != nil {
		t.Fatal("force did not override the hold")
	}
	r = dispatchJSON(t, h, "reserve", map[string]any{"device": id, "owner": "lab", "force": true})
	if r.IsError {
		t.Fatalf("forced reserve: %s", resultText(t, &r))
	}
	r = dispatchJSON(t, h, "reserve", map[string]any{"device": id, "owner": "lab"})
	if r.IsError {
		t.Fatalf("holder's renewal refused: %s", resultText(t, &r))
	}

	// Recharging on a better port: still held below 30%, free at 30%.
	drainSamples(h, id, now.Add(10*time.Minute), true, 12, 16, 20, 24, 28)
	if tr := h.trend(id); tr.State != device.DrainRecharging || h.leaseRefusal(id) == "" {
		t.Fatalf("recharging: %+v", tr)
	}
	drainSamples(h, id, now.Add(11*time.Minute), true, 31)
	if why := h.leaseRefusal(id); why != "" {
		t.Fatalf("still refused at 31%%: %s", why)
	}
}

// Heavy load far from empty is never refused.
func TestPowerPolicy_HeavyLoadIsAllowed(t *testing.T) {
	h, _ := newHandlerWithReservations(t, nil, nil)
	const id = "R58TESTHEAVY"
	drainSamples(h, id, time.Now(), true, 90, 89, 88, 87, 86, 85, 84, 83, 82, 81, 80)
	if why := h.leaseRefusal(id); why != "" {
		t.Fatalf("refused: %s", why)
	}
	if tr := h.trend(id); tr.State != device.DrainOK {
		t.Fatalf("trend: %+v", tr)
	}
}

func TestLeaseRefusal_LockedAndUnpowered(t *testing.T) {
	h, _ := newHandlerWithReservations(t, nil, nil)
	h.lastPower = map[string]powerReading{
		"locked": {power: device.Power{Health: device.PowerLockedSecure}, at: time.Now()},
		"flat":   {power: device.Power{Health: device.PowerNotPowered}, at: time.Now()},
		"full":   {power: device.Power{Health: device.PowerNotPowered}, at: time.Now()},
	}
	now := time.Now()
	drainSamples(h, "flat", now, false, 25)
	drainSamples(h, "full", now, false, 80)
	if h.leaseRefusal("locked") == "" || h.leaseRefusal("flat") == "" {
		t.Fatal("locked or unpowered-and-low device admitted")
	}
	if why := h.leaseRefusal("full"); why != "" {
		t.Fatalf("unpowered at 80%% refused: %s", why)
	}
}

// The idle policy measures from the last lease or verb; a lease keeps
// the device active (🎯T194.2).
func TestLaunchRegistry_IdleAndTouch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launched.json")
	r := newLaunchRegistry(path)
	t0 := time.Now()
	r.Note("dev", "com.game", t0)
	if got := r.Idle(t0.Add(idleStopAfter-time.Second), idleStopAfter); len(got) != 0 {
		t.Fatalf("idle too early: %v", got)
	}
	r.Touch("dev", t0.Add(5*time.Minute))
	if got := r.Idle(t0.Add(idleStopAfter+time.Second), idleStopAfter); len(got) != 0 {
		t.Fatalf("touch ignored: %v", got)
	}
	if got := r.Idle(t0.Add(16*time.Minute), idleStopAfter); len(got) != 1 || got[0].BundleID != "com.game" {
		t.Fatalf("idle: %v", got)
	}
	// Survives a restart; forgetting removes it.
	if got := newLaunchRegistry(path).OnDevice("dev"); len(got) != 1 {
		t.Fatalf("reload: %v", got)
	}
	r.Forget("dev", "com.game")
	if got := newLaunchRegistry(path).OnDevice("dev"); len(got) != 0 {
		t.Fatalf("forget: %v", got)
	}
	var nilReg *launchRegistry
	nilReg.Note("d", "b", t0) // nil-safe
}

func TestSweepIdle_LeaseKeepsAppsRunning(t *testing.T) {
	h, s := newHandlerWithReservations(t, nil, nil)
	t0 := time.Now()
	h.launched.Note("R58TESTLEASE", "com.game", t0.Add(-time.Hour))
	if _, err := s.Acquire("R58TESTLEASE", "lab", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	h.sweepIdle(t0)
	if got := h.launched.Idle(t0, idleStopAfter); len(got) != 0 {
		t.Fatalf("leased device treated as idle: %v", got)
	}
}

// After an adb server restart spyder puts its reverse mappings back,
// retrying while the device reconnects (🎯T194.5).
func TestRestoreReverses_RetriesUntilDeviceReturns(t *testing.T) {
	h := &Handler{}
	h.noteReverse("R58TESTREV", 41234, true)
	h.noteReverse("R58TESTGONE", 41235, true)
	h.noteReverse("R58TESTGONE", 41235, false)
	calls := 0
	orig := reverseAppChannel
	t.Cleanup(func() { reverseAppChannel = orig })
	reverseAppChannel = func(id string, port int) error {
		calls++
		if id != "R58TESTREV" || port != 41234 {
			t.Errorf("unexpected reverse %s:%d", id, port)
		}
		if calls < 2 {
			return errors.New("device 'R58TESTREV' not found")
		}
		return nil
	}
	h.restoreReverses(adbserver.Event{Down: time.Now(), Up: time.Now()}, []adbReverse{{"R58TESTREV", 41234}})
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	h.reverseMu.Lock()
	n := len(h.reverses)
	h.reverseMu.Unlock()
	if n != 1 {
		t.Fatalf("tracked reverses %d", n)
	}
}
