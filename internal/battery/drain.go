// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package battery

import (
	"fmt"
	"math"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// Drain policy (🎯T194.3). Heavy loads may outrun a hub port for a
// while; spyder steps in only when a device is heading for empty.
const (
	// DrainFloor is the level spyder protects. A device at or below it
	// that is still falling has its apps stopped and is held.
	DrainFloor = 10
	// DrainResume is the level a held device must recover to before it
	// takes new leases or launches again.
	DrainResume = 30
	// DrainWindow is how far back the trend fit looks.
	DrainWindow = 20 * time.Minute
	// drainMinSpan is the shortest history worth fitting a trend to.
	drainMinSpan = 8 * time.Minute
	// DrainHorizon: a device projected to reach the floor within this is
	// at risk.
	DrainHorizon = 45 * time.Minute
	// DrainLookback is how much history Assess needs to remember a hold.
	DrainLookback = 12 * time.Hour
	// drainStale: a latest sample older than this means the device is not
	// being sampled (disconnected), so no trend is reported.
	drainStale = 5 * time.Minute
)

// Point is one level observation.
type Point struct {
	TS       time.Time
	Level    int
	Charging bool // charging or full
}

// PointFromSample reduces a stored sample to a Point. ok is false when
// the sample has no level.
func PointFromSample(s Sample) (Point, bool) {
	if s.BatteryLevel == nil {
		return Point{}, false
	}
	p := Point{TS: s.TS, Level: *s.BatteryLevel}
	switch {
	case s.Power != nil && s.Power.Status != "":
		p.Charging = s.Power.Status == device.ChargeCharging || s.Power.Status == device.ChargeFull
	case s.Charging != nil:
		p.Charging = *s.Charging
	}
	return p, true
}

// Assess projects one device's trend from its points, oldest first.
// It returns nil when the latest point is stale.
func Assess(points []Point, now time.Time) *device.PowerTrend {
	if len(points) == 0 {
		return nil
	}
	latest := points[len(points)-1]
	if now.Sub(latest.TS) > drainStale {
		return nil
	}
	t := &device.PowerTrend{State: device.DrainOK, Level: latest.Level}

	// Fit the level over the window.
	var window []Point
	for _, p := range points {
		if !p.TS.Before(latest.TS.Add(-DrainWindow)) {
			window = append(window, p)
		}
	}
	if len(window) >= 3 && latest.TS.Sub(window[0].TS) >= drainMinSpan {
		rate := slopePerHour(window)
		t.RatePctPerHour = &rate
		if rate < 0 && latest.Level > DrainFloor {
			m := int(math.Round(float64(latest.Level-DrainFloor) / -rate * 60))
			t.MinutesToFloor = &m
		}
	}
	falling := t.RatePctPerHour != nil && *t.RatePctPerHour < 0
	if t.RatePctPerHour == nil {
		falling = !latest.Charging
	}

	// A hold starts when the level reaches the floor and lasts until a
	// later point is back at DrainResume.
	var floorAt *time.Time
	for _, p := range points {
		switch {
		case p.Level <= DrainFloor:
			ts := p.TS
			floorAt = &ts
		case p.Level >= DrainResume:
			floorAt = nil
		}
	}
	if floorAt != nil {
		t.FloorAt = floorAt
		if latest.Level <= DrainFloor && falling {
			t.State = device.DrainCritical
			t.Reason = fmt.Sprintf("at %d%% and still falling; spyder stops its apps and holds it until it is back to %d%%", latest.Level, DrainResume)
		} else {
			t.State = device.DrainRecharging
			t.Reason = fmt.Sprintf("reached %d%% at %s; held until it is back to %d%% (now %d%%)", DrainFloor, floorAt.Local().Format("15:04"), DrainResume, latest.Level)
		}
		return t
	}
	if t.MinutesToFloor != nil && time.Duration(*t.MinutesToFloor)*time.Minute <= DrainHorizon {
		t.State = device.DrainAtRisk
		t.Reason = fmt.Sprintf("falling %.0f%%/h; reaches %d%% in about %d min", -*t.RatePctPerHour, DrainFloor, *t.MinutesToFloor)
	}
	return t
}

// slopePerHour is the least-squares level slope in percent per hour.
func slopePerHour(points []Point) float64 {
	t0 := points[0].TS
	var n, sx, sy, sxx, sxy float64
	for _, p := range points {
		x := p.TS.Sub(t0).Hours()
		y := float64(p.Level)
		n++
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}
