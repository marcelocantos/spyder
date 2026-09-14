// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import "testing"

func TestEvaluate_SolidHypothesis(t *testing.T) {
	in := HypothesisInput{
		Wifi:           Status{Device: "en0", PowerOn: true, SSID: "Home", Connected: true},
		GatewayOK:      false,
		UsbWedge:       false,
		DeviceStress:   DeviceStress{LocalNetworkDevices: 1},
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
		GatewayOK:      false,
		DeviceStress:   DeviceStress{LANWorkflow: true},
		ConsecutiveBad: 0,
	}
	if Evaluate(in).Solid {
		t.Fatal("first cycle should not be solid")
	}
}

func TestEvaluate_ExcludesUsbWedge(t *testing.T) {
	in := HypothesisInput{
		Wifi:           Status{Connected: true, SSID: "Home"},
		GatewayOK:      false,
		UsbWedge:       true,
		DeviceStress:   DeviceStress{LANWorkflow: true},
		ConsecutiveBad: 2,
	}
	if Evaluate(in).Solid {
		t.Fatal("usb wedge should exclude Wi-Fi hypothesis")
	}
}

func TestEvaluate_GatewayOK(t *testing.T) {
	in := HypothesisInput{
		Wifi:           Status{Connected: true, SSID: "Home"},
		GatewayOK:      true,
		DeviceStress:   DeviceStress{LANWorkflow: true},
		ConsecutiveBad: 2,
	}
	if Evaluate(in).Solid {
		t.Fatal("reachable gateway should not trigger")
	}
}
