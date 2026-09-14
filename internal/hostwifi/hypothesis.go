// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"fmt"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

// HypothesisInput is the evidence gathered for one monitor cycle.
type HypothesisInput struct {
	Wifi           Status
	AppChannel     appchannel.ConnectivityReport
	UsbWedge       bool // usbmux parity wedge — different remediation
	ConsecutiveBad int  // prior cycles that also matched partial criteria
}

// Hypothesis is the monitor's verdict for one cycle.
type Hypothesis struct {
	Solid      bool
	Detail     string
	UserPrompt string // non-empty when Solid — shown in the confirmation dialog
}

// Evaluate decides whether Wi-Fi is the likely cause of device connectivity
// loss. The primary signal is app-channel staleness: devices that should
// have dialled back to spyder (or respond to ping on a live session) but
// haven't. OS-level probes are deliberately avoided — outbound/inbound
// self-dials can succeed while the device→host path is broken.
func Evaluate(in HypothesisInput) Hypothesis {
	if in.UsbWedge {
		return Hypothesis{Detail: "usbmux wedge active — not Wi-Fi"}
	}
	if !in.Wifi.Connected {
		return Hypothesis{Detail: "Wi-Fi not associated"}
	}
	if !in.AppChannel.StressPresent() {
		return Hypothesis{Detail: "app-channel paths healthy"}
	}

	if in.ConsecutiveBad < 1 {
		return Hypothesis{
			Detail: fmt.Sprintf(
				"app-channel staleness on %q (missing=%d ping_fail=%d); awaiting confirmation cycle",
				in.Wifi.SSID,
				in.AppChannel.MissingDialBacks,
				in.AppChannel.PingFailures,
			),
		}
	}

	detail := fmt.Sprintf(
		"Wi-Fi on %q: %d device(s) not reaching spyder via app-channel",
		in.Wifi.SSID,
		in.AppChannel.MissingDialBacks+in.AppChannel.PingFailures,
	)
	if in.AppChannel.MissingDialBacks > 0 && in.AppChannel.PingFailures > 0 {
		detail = fmt.Sprintf(
			"Wi-Fi on %q: %d missing dial-back(s), %d ping failure(s)",
			in.Wifi.SSID, in.AppChannel.MissingDialBacks, in.AppChannel.PingFailures,
		)
	} else if in.AppChannel.MissingDialBacks > 0 {
		detail = fmt.Sprintf(
			"Wi-Fi on %q: %d app(s) launched but never dialled back",
			in.Wifi.SSID, in.AppChannel.MissingDialBacks,
		)
	} else {
		detail = fmt.Sprintf(
			"Wi-Fi on %q: %d app-channel ping failure(s)",
			in.Wifi.SSID, in.AppChannel.PingFailures,
		)
	}

	prompt := fmt.Sprintf(
		"Spyder detected that %d device app(s) cannot reach this Mac over Wi-Fi (%q).\n\n"+
			"Your laptop may still work normally — this is an inbound connectivity problem.\n\n"+
			"Bounce Wi-Fi now? You will briefly lose connectivity.",
		in.AppChannel.MissingDialBacks+in.AppChannel.PingFailures,
		in.Wifi.SSID,
	)
	return Hypothesis{
		Solid:      true,
		Detail:     detail,
		UserPrompt: prompt,
	}
}
