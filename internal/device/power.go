// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"time"
)

// Power and lock health (🎯T194.1).
//
// The lab's failure mode is not screen state: devices charge with the
// screen on. A game left running drains a device faster than its hub
// port can charge it, and a device that locks behind a PIN cannot run a
// journey. Power reports both, per device, from what each platform says.

// Power health values, worst first.
const (
	PowerLockedSecure = "locked_secure" // keyguard up behind a credential
	PowerNotPowered   = "not_powered"   // no power source at all
	PowerOffline      = "offline"       // no validated default network
	PowerHot          = "hot"           // battery at or above powerHotC
	PowerDraining     = "draining"      // powered, yet discharging
	PowerOK           = "ok"
	PowerUnknown      = "unknown" // nothing to judge by
)

// powerHotC is the battery temperature at which a device is reported hot.
// Phones throttle and stop charging a little above this.
const powerHotC = 40.0

// NetworkNone is DefaultNetwork when the device has no network.
const NetworkNone = "none"

// Power sources.
const (
	PowerSourceAC       = "ac"
	PowerSourceUSB      = "usb"
	PowerSourceWireless = "wireless"
	PowerSourceDock     = "dock"
	PowerSourceExternal = "external" // iOS reports only "connected"
	PowerSourceNone     = "none"
)

// Charge statuses.
const (
	ChargeCharging    = "charging"
	ChargeDischarging = "discharging"
	ChargeNotCharging = "not_charging" // powered but held (e.g. Samsung's 80% battery protection)
	ChargeFull        = "full"
)

// Power is one device's power and lock state. Pointer and empty fields
// were not observed.
type Power struct {
	Source       string   `json:"source,omitempty"`
	Status       string   `json:"status,omitempty"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	// CurrentMA is the battery current in mA, negative when discharging.
	// iOS only: Android vendors disagree on units and sign.
	CurrentMA *int `json:"current_ma,omitempty"`

	// Wakefulness is Android's screen power state (Awake, Asleep, Dozing,
	// Dreaming). StayOn is whether stay-awake-while-plugged is in effect.
	Wakefulness string `json:"wakefulness,omitempty"`
	StayOn      *bool  `json:"stay_on,omitempty"`

	// KeyguardShowing: the lock screen is up. DeviceLocked: the user must
	// authenticate to get past it. CredentialSet: a PIN, pattern or
	// password exists (Android). On iOS, DeviceLocked is devicectl's
	// passcodeRequired from the automation supervisor's last probe.
	KeyguardShowing *bool `json:"keyguard_showing,omitempty"`
	DeviceLocked    *bool `json:"device_locked,omitempty"`
	CredentialSet   *bool `json:"credential_set,omitempty"`

	// Network reachability (Android). DefaultNetwork is the transport of
	// the default network (wifi, cellular, ...) or "none";
	// NetworkValidated is whether Android has confirmed it reaches the
	// internet.
	AirplaneMode     *bool  `json:"airplane_mode,omitempty"`
	WifiOn           *bool  `json:"wifi_on,omitempty"`
	DefaultNetwork   string `json:"default_network,omitempty"`
	NetworkValidated *bool  `json:"network_validated,omitempty"`

	// Health is the worst problem found; Issues lists every one.
	Health string   `json:"power_health"`
	Issues []string `json:"issues,omitempty"`

	// Trend is the drain projection from battery history (🎯T194.3).
	// It is set by the daemon, not by the adapters.
	Trend *PowerTrend `json:"trend,omitempty"`
}

// Classify sets Health and Issues from the observed fields.
func (p *Power) Classify() {
	p.Issues = nil
	health := ""
	note := func(h, msg string) {
		if health == "" {
			health = h
		}
		p.Issues = append(p.Issues, h+": "+msg)
	}
	if p.lockedSecure() {
		note(PowerLockedSecure, "the lock screen is up behind a PIN, pattern or password; unlock it or remove the lock")
	}
	if p.Source == PowerSourceNone {
		note(PowerNotPowered, "no power source; check the cable, port and hub")
	}
	if p.NetworkValidated != nil && !*p.NetworkValidated {
		msg := "no network reaches the internet"
		switch {
		case p.AirplaneMode != nil && *p.AirplaneMode:
			msg += "; airplane mode is on"
		case p.WifiOn != nil && !*p.WifiOn:
			msg += "; Wi-Fi is off"
		case p.DefaultNetwork != NetworkNone:
			msg = "the default network (" + p.DefaultNetwork + ") is not validated; it may not reach the internet"
		}
		if p.AirplaneMode != nil && *p.AirplaneMode && p.WifiOn != nil && *p.WifiOn && p.DefaultNetwork != NetworkNone {
			msg = "the default network is not validated (airplane mode is on with Wi-Fi enabled)"
		}
		note(PowerOffline, msg)
	}
	if p.TemperatureC != nil && *p.TemperatureC >= powerHotC {
		note(PowerHot, fmt.Sprintf("battery at %.1f °C", *p.TemperatureC))
	}
	if p.Source != "" && p.Source != PowerSourceNone && p.Status == ChargeDischarging {
		note(PowerDraining, "plugged in but discharging; the port cannot keep up with the load")
	}
	switch {
	case health != "":
		p.Health = health
	case p.Source == "" && p.Status == "" && p.TemperatureC == nil && p.DeviceLocked == nil:
		p.Health = PowerUnknown
	default:
		p.Health = PowerOK
	}
}

func (p *Power) lockedSecure() bool {
	if p.DeviceLocked != nil && *p.DeviceLocked {
		// Android sets CredentialSet; iOS only reports locked when a
		// passcode is required.
		return p.CredentialSet == nil || *p.CredentialSet
	}
	return p.KeyguardShowing != nil && *p.KeyguardShowing && p.CredentialSet != nil && *p.CredentialSet
}

// Drain trend states (🎯T194.3). A device in DrainCritical or
// DrainRecharging is held out of new leases and launches until it is
// back to the resume level.
const (
	DrainOK         = "ok"
	DrainAtRisk     = "at_risk"    // falling fast enough to reach the floor soon
	DrainCritical   = "critical"   // at or below the floor and still falling
	DrainRecharging = "recharging" // reached the floor; held until it recovers
)

// PowerTrend is the battery trend spyder projects from the minute
// samples in battery history.
type PowerTrend struct {
	State string `json:"state"`
	Level int    `json:"level"`
	// RatePctPerHour is the fitted level change; negative when falling.
	RatePctPerHour *float64 `json:"rate_pct_per_hour,omitempty"`
	// MinutesToFloor projects when a falling level reaches the floor.
	MinutesToFloor *int `json:"minutes_to_floor,omitempty"`
	// FloorAt is when the level last reached the floor (held states).
	FloorAt *time.Time `json:"floor_at,omitempty"`
	Reason  string     `json:"reason,omitempty"`
}

// Held reports whether the trend keeps the device out of new work.
func (t *PowerTrend) Held() bool {
	return t != nil && (t.State == DrainCritical || t.State == DrainRecharging)
}
