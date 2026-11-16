// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

// iOS power and lock state (🎯T194.1).

// iosPower builds Power from lockdown's battery domain and the
// IORegistry keys State already collected. IORegistry reports
// Temperature in hundredths of a degree and InstantAmperage in mA
// (negative while discharging); a zero temperature means not reported.
func iosPower(external, charging, full bool, extra map[string]any) *Power {
	p := &Power{Source: PowerSourceNone}
	if external {
		p.Source = PowerSourceExternal
	}
	amps, haveAmps := extra["InstantAmperage"].(int)
	if haveAmps {
		p.CurrentMA = &amps
	}
	switch {
	case full:
		p.Status = ChargeFull
	case charging:
		p.Status = ChargeCharging
	case !external || (haveAmps && amps < 0):
		p.Status = ChargeDischarging
	default:
		p.Status = ChargeNotCharging
	}
	if t, ok := extra["Temperature"].(int); ok && t > 0 {
		c := float64(t) / 100
		p.TemperatureC = &c
	}
	p.Classify()
	return p
}

// fillLock overlays the automation supervisor's last lock read. It is
// not cached with the battery: lock changes inside stateTTL.
func (a *IOSAdapter) fillLock(id string, st *State) {
	if st == nil || st.Power == nil || a == nil || a.automation == nil {
		return
	}
	p := *st.Power
	if locked, ok := a.automation.LastLock(id); ok {
		p.DeviceLocked = &locked
	} else {
		p.DeviceLocked = nil
	}
	p.Classify()
	st.Power = &p
}
