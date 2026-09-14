// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import "fmt"

// HypothesisInput is the evidence gathered for one monitor cycle.
type HypothesisInput struct {
	Wifi           Status
	GatewayOK      bool
	UsbWedge       bool // usbmux parity wedge — different remediation
	DeviceStress   DeviceStress
	ConsecutiveBad int // prior cycles that also matched partial criteria
}

// Hypothesis is the monitor's verdict for one cycle.
type Hypothesis struct {
	Solid      bool
	Detail     string
	UserPrompt string // non-empty when Solid — shown in the confirmation dialog
}

// Evaluate decides whether Wi-Fi is the likely cause of device connectivity
// loss. A "solid" hypothesis requires zombie Wi-Fi (associated but gateway
// unreachable), exclusion of the usbmux wedge, device/LAN stress, and
// persistence across consecutiveBad prior cycles (caller increments).
func Evaluate(in HypothesisInput) Hypothesis {
	if in.UsbWedge {
		return Hypothesis{Detail: "usbmux wedge active — not Wi-Fi"}
	}
	if !in.Wifi.Connected {
		return Hypothesis{Detail: "Wi-Fi not associated"}
	}
	if in.GatewayOK {
		return Hypothesis{Detail: "gateway reachable"}
	}

	stress := deviceStressPresent(in.DeviceStress)
	if !stress {
		return Hypothesis{Detail: "no device/LAN stress signal"}
	}

	// Require two consecutive bad readings before surfacing — filters brief
	// DHCP renewals and transient router hiccups.
	if in.ConsecutiveBad < 1 {
		return Hypothesis{
			Detail: fmt.Sprintf("zombie Wi-Fi suspected (SSID %q); awaiting confirmation cycle", in.Wifi.SSID),
		}
	}

	prompt := fmt.Sprintf(
		"Spyder suspects your Mac's Wi-Fi (%q) has lost connectivity — "+
			"devices may be unable to reach this machine.\n\n"+
			"Bounce Wi-Fi now? You will briefly lose internet access.",
		in.Wifi.SSID,
	)
	return Hypothesis{
		Solid:      true,
		Detail:     fmt.Sprintf("zombie Wi-Fi on %q: associated but gateway unreachable", in.Wifi.SSID),
		UserPrompt: prompt,
	}
}

func deviceStressPresent(d DeviceStress) bool {
	if d.LocalNetworkDevices > 0 {
		return true
	}
	if d.TunnelFailures > 0 {
		return true
	}
	return d.LANWorkflow
}
