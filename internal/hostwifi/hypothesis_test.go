// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"testing"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func TestEvaluate_SolidHypothesis(t *testing.T) {
	in := HypothesisInput{
		Wifi:           Status{Device: "en0", Connected: true, SSID: "Home"},
		AppChannel:     appchannel.ConnectivityReport{PingFailures: 1},
		ConsecutiveBad: 1,
	}
	h := Evaluate(in)
	if !h.Solid {
		t.Fatalf("Solid = false; want true (detail=%q)", h.Detail)
	}
	if h.UserPrompt == "" {
		t.Fatal("UserPrompt empty")
	}
}

func TestEvaluate_RequiresPersistence(t *testing.T) {
	in := HypothesisInput{
		Wifi:           Status{Connected: true, SSID: "Home"},
		AppChannel:     appchannel.ConnectivityReport{MissingDialBacks: 1},
		ConsecutiveBad: 0,
	}
	if Evaluate(in).Solid {
		t.Fatal("first cycle should not be solid")
	}
}

func TestEvaluate_ExcludesUsbWedge(t *testing.T) {
	in := HypothesisInput{
		Wifi:           Status{Connected: true, SSID: "Home"},
		AppChannel:     appchannel.ConnectivityReport{PingFailures: 1},
		UsbWedge:       true,
		ConsecutiveBad: 2,
	}
	if Evaluate(in).Solid {
		t.Fatal("usb wedge should exclude Wi-Fi hypothesis")
	}
}

func TestEvaluate_HealthyAppChannel(t *testing.T) {
	in := HypothesisInput{
		Wifi:           Status{Connected: true, SSID: "Home"},
		AppChannel:     appchannel.ConnectivityReport{},
		ConsecutiveBad: 2,
	}
	if Evaluate(in).Solid {
		t.Fatal("healthy app-channel should not trigger")
	}
}
