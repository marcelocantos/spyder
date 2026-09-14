// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import "testing"

func TestParseLocalNetworkIOS(t *testing.T) {
	data := []byte(`{
		"result": {
			"devices": [
				{
					"hardwareProperties": {"udid": "WIRED-IOS", "platform": "iOS"},
					"connectionProperties": {"tunnelState": "connected", "transportType": "wired"}
				},
				{
					"hardwareProperties": {"udid": "WIFI-IOS", "platform": "iOS"},
					"connectionProperties": {"tunnelState": "connected", "transportType": "localNetwork"}
				}
			]
		}
	}`)
	got := parseLocalNetworkIOS(data)
	if len(got) != 1 || got[0] != "WIFI-IOS" {
		t.Fatalf("got %v; want [WIFI-IOS]", got)
	}
}
