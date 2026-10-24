// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"reflect"
	"testing"
)

func TestUSBNetworkInterfacesForSerial(t *testing.T) {
	raw, err := os.ReadFile("testdata/ioreg_usb_ncm.txt")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		udid string
		want []string
	}{
		// Dashed UDID matches the dashless USB serial; both NCM interfaces
		// belong to the phone (directly or through an interface node).
		{"00008110-0014182E0AC2801E", []string{"en8", "en12"}},
		{"000081300009702e1110001c", []string{"en7"}},
		// A hub does not claim its children's interfaces, only its own.
		{"HUB0001", []string{"en5"}},
		{"00000000-0000000000000000", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := usbNetworkInterfacesForSerial(string(raw), c.udid)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("udid %q: got %v, want %v", c.udid, got, c.want)
		}
	}
}
