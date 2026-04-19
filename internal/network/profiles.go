// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package network defines named network condition profiles and the
// NetworkProfile struct used by device adapters to apply them.
//
// # Supported profiles
//
// Named presets:
//
//	wifi    — full-speed (no artificial throttle)
//	4g      — HSPA+ class: 14 Mbps down / 5.76 Mbps up, 20 ms delay
//	3g      — UMTS class:  2 Mbps  down / 384 kbps up,  100 ms delay
//	edge    — EDGE/2.75G:  384 kbps down / 128 kbps up, 400 ms delay
//	gsm     — GPRS:        114 kbps down / 40 kbps  up, 600 ms delay
//	offline — no connectivity (speed=0, delay=0)
//
// Dynamic patterns (regex-parsed):
//
//	lossy-<pct>  — no speed limit, random <pct>% packet loss (0–100)
//	delay-<ms>   — no speed limit, extra one-way <ms> latency (≥0)
//
// Dynamic patterns may be combined with a named preset via profile
// composition in the handler layer, but each profile string is
// independently valid.
package network

import (
	"fmt"
	"regexp"
	"strconv"
)

// NetworkProfile holds all the shaping parameters for a named profile.
// Zero values mean "no limit / no change"; callers must check IsOffline
// separately to distinguish "full speed" from "not set".
type NetworkProfile struct {
	// Name is the canonical profile name (as supplied by the caller,
	// after normalisation).
	Name string

	// UploadKbps / DownloadKbps set the throttle ceiling. 0 means
	// full-speed (no throttle). Positive values are kbits per second.
	UploadKbps   int
	DownloadKbps int

	// DelayMs is one-way added latency in milliseconds. 0 = no extra
	// delay.
	DelayMs int

	// LossPct is the random packet-loss percentage (0–100). 0 = no
	// loss.
	LossPct int

	// IsOffline means no connectivity at all (speed=0, delay=0, loss
	// not meaningful). Adapters should drop all traffic.
	IsOffline bool
}

var (
	lossyRE = regexp.MustCompile(`^lossy-(\d+)$`)
	delayRE = regexp.MustCompile(`^delay-(\d+)$`)
)

// Parse resolves a profile name string to a NetworkProfile. Returns an
// error when the name is not recognised.
func Parse(name string) (NetworkProfile, error) {
	switch name {
	case "wifi":
		return NetworkProfile{Name: name}, nil

	case "4g":
		// HSPA+ class — broadly matches Apple's "LTE" Link Conditioner
		// profile and the adb "hsdpa" speed class.
		return NetworkProfile{
			Name:         name,
			UploadKbps:   5760,  // 5.76 Mbps
			DownloadKbps: 14400, // 14.4 Mbps
			DelayMs:      20,
		}, nil

	case "3g":
		// UMTS/WCDMA class — adb "umts".
		return NetworkProfile{
			Name:         name,
			UploadKbps:   384,
			DownloadKbps: 2000,
			DelayMs:      100,
		}, nil

	case "edge":
		// EDGE/2.75G — adb "edge".
		return NetworkProfile{
			Name:         name,
			UploadKbps:   128,
			DownloadKbps: 384,
			DelayMs:      400,
		}, nil

	case "gsm":
		// GPRS/GSM — adb "gprs".
		return NetworkProfile{
			Name:         name,
			UploadKbps:   40,
			DownloadKbps: 114,
			DelayMs:      600,
		}, nil

	case "offline":
		return NetworkProfile{Name: name, IsOffline: true}, nil
	}

	// Dynamic: lossy-<pct>
	if m := lossyRE.FindStringSubmatch(name); m != nil {
		pct, _ := strconv.Atoi(m[1])
		if pct < 0 || pct > 100 {
			return NetworkProfile{}, fmt.Errorf("network profile %q: loss percentage must be 0–100", name)
		}
		return NetworkProfile{Name: name, LossPct: pct}, nil
	}

	// Dynamic: delay-<ms>
	if m := delayRE.FindStringSubmatch(name); m != nil {
		ms, _ := strconv.Atoi(m[1])
		if ms < 0 {
			return NetworkProfile{}, fmt.Errorf("network profile %q: delay must be ≥ 0", name)
		}
		return NetworkProfile{Name: name, DelayMs: ms}, nil
	}

	return NetworkProfile{}, fmt.Errorf(
		"unknown network profile %q — supported: wifi, 4g, 3g, edge, gsm, offline, lossy-<pct>, delay-<ms>",
		name,
	)
}

// ADBSpeedClass maps a NetworkProfile to the adb emulator network speed
// keyword. Returns the keyword and whether a speed-class shorthand exists.
// When no shorthand exists (e.g. wifi / dynamic profiles), callers should
// use numeric kbps values instead.
func ADBSpeedClass(p NetworkProfile) (keyword string, ok bool) {
	switch p.Name {
	case "4g":
		return "hsdpa", true
	case "3g":
		return "umts", true
	case "edge":
		return "edge", true
	case "gsm":
		return "gprs", true
	}
	return "", false
}

// ADBDelayClass maps a NetworkProfile to the adb emulator network delay
// keyword. Returns the keyword and whether a delay-class shorthand exists.
func ADBDelayClass(p NetworkProfile) (keyword string, ok bool) {
	switch p.Name {
	case "4g":
		// adb delay classes: gprs/edge/umts/none. 4g sits between umts
		// and "none" — use numeric value for accuracy.
		return "", false
	case "3g":
		return "umts", true
	case "edge":
		return "edge", true
	case "gsm":
		return "gprs", true
	}
	return "", false
}
