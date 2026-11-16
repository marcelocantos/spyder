// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import "fmt"

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
	PowerHot          = "hot"           // battery at or above powerHotC
	PowerDraining     = "draining"      // powered, yet discharging
	PowerOK           = "ok"
	PowerUnknown      = "unknown" // nothing to judge by
)

// powerHotC is the battery temperature at which a device is reported hot.
// Phones throttle and stop charging a little above this.
const powerHotC = 40.0

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

	// Health is the worst problem found; Issues lists every one.
	Health string   `json:"power_health"`
	Issues []string `json:"issues,omitempty"`
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
