// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package battery

import (
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

var drainT0 = time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)

// series returns one point a minute from drainT0 with the given levels.
func series(charging bool, levels ...int) []Point {
	out := make([]Point, len(levels))
	for i, l := range levels {
		out[i] = Point{TS: drainT0.Add(time.Duration(i) * time.Minute), Level: l, Charging: charging}
	}
	return out
}

func last(ps []Point) time.Time { return ps[len(ps)-1].TS }

func TestAssess_HeavyLoadFarFromEmptyIsOK(t *testing.T) {
	// 80% falling 1%/min under a game: 70 min to the floor.
	ps := series(true, 90, 89, 88, 87, 86, 85, 84, 83, 82, 81, 80)
	tr := Assess(ps, last(ps))
	if tr.State != device.DrainOK || tr.MinutesToFloor == nil || *tr.MinutesToFloor != 70 {
		t.Fatalf("%+v", tr)
	}
}

func TestAssess_AtRisk(t *testing.T) {
	ps := series(true, 40, 39, 38, 37, 36, 35, 34, 33, 32, 31, 30)
	tr := Assess(ps, last(ps))
	if tr.State != device.DrainAtRisk || *tr.MinutesToFloor != 20 {
		t.Fatalf("%+v", tr)
	}
}

func TestAssess_CriticalThenRechargingThenReleased(t *testing.T) {
	ps := series(false, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9)
	tr := Assess(ps, last(ps))
	if tr.State != device.DrainCritical || !tr.Held() || tr.FloorAt == nil {
		t.Fatalf("critical: %+v", tr)
	}
	// Plugged into a better port: rising, but still held below resume.
	for _, l := range []int{10, 12, 15, 18, 22, 25, 28} {
		ps = append(ps, Point{TS: last(ps).Add(time.Minute), Level: l, Charging: true})
	}
	tr = Assess(ps, last(ps))
	if tr.State != device.DrainRecharging || !tr.Held() {
		t.Fatalf("recharging: %+v", tr)
	}
	ps = append(ps, Point{TS: last(ps).Add(time.Minute), Level: 30, Charging: true})
	tr = Assess(ps, last(ps))
	if tr.Held() || tr.State != device.DrainOK {
		t.Fatalf("released: %+v", tr)
	}
}

func TestAssess_AtFloorButChargingIsNotCritical(t *testing.T) {
	ps := series(true, 6, 7, 7, 8, 8, 9, 9, 10, 10)
	tr := Assess(ps, last(ps))
	if tr.State != device.DrainRecharging {
		t.Fatalf("%+v", tr)
	}
}

func TestAssess_StaleAndShortHistory(t *testing.T) {
	ps := series(false, 50, 49)
	if tr := Assess(ps, last(ps).Add(10*time.Minute)); tr != nil {
		t.Fatalf("stale: %+v", tr)
	}
	tr := Assess(ps, last(ps))
	if tr == nil || tr.RatePctPerHour != nil || tr.State != device.DrainOK {
		t.Fatalf("short: %+v", tr)
	}
	// No fit, not charging, at the floor: critical on status alone.
	ps = series(false, 9)
	if tr := Assess(ps, last(ps)); tr.State != device.DrainCritical {
		t.Fatalf("status only: %+v", tr)
	}
}

func TestPointFromSample_PrefersPowerStatus(t *testing.T) {
	lvl, no := 50, false
	s := Sample{TS: drainT0, BatteryLevel: &lvl, Charging: &no, Power: &device.Power{Status: device.ChargeFull}}
	p, ok := PointFromSample(s)
	if !ok || !p.Charging {
		t.Fatalf("%+v", p)
	}
	if _, ok := PointFromSample(Sample{}); ok {
		t.Fatal("no level")
	}
}
