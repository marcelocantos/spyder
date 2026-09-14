// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// DeviceStress captures signals that devices may be unable to reach the host
// or be reached over the LAN — used to strengthen a Wi-Fi hypothesis.
type DeviceStress struct {
	LocalNetworkDevices int  // devicectl localNetwork+connected iOS devices
	LANWorkflow         bool // mobile inventory + non-loopback daemon bind
	TunnelFailures      int  // health-model devices in degraded+ with tunnel evidence
}

// ProbeDeviceStress queries devicectl for Wi-Fi-attached iOS devices and
// optionally scans the health model for tunnel-layer failures.
func ProbeDeviceStress(iosBinary string, lanWorkflow bool, tunnelFailures int) DeviceStress {
	st := DeviceStress{
		LANWorkflow:    lanWorkflow,
		TunnelFailures: tunnelFailures,
	}
	if n, err := countLocalNetworkIOSDevices(); err == nil {
		st.LocalNetworkDevices = n
	}
	_ = iosBinary // reserved for future usbmux cross-check
	return st
}

func countLocalNetworkIOSDevices() (int, error) {
	tmp, err := os.MkdirTemp("", "spyder-hostwifi-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)

	path := filepath.Join(tmp, "devices.json")
	cmd := exec.Command("xcrun", "devicectl", "list", "devices", "--quiet", "--json-output", path)
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("devicectl list devices: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return len(parseLocalNetworkIOS(data)), nil
}

func parseLocalNetworkIOS(data []byte) []string {
	var parsed struct {
		Result struct {
			Devices []struct {
				ConnectionProperties struct {
					TunnelState   string `json:"tunnelState"`
					TransportType string `json:"transportType"`
				} `json:"connectionProperties"`
				HardwareProperties struct {
					UDID     string `json:"udid"`
					Platform string `json:"platform"`
				} `json:"hardwareProperties"`
			} `json:"devices"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	var out []string
	for _, d := range parsed.Result.Devices {
		if d.HardwareProperties.Platform != "iOS" {
			continue
		}
		if d.ConnectionProperties.TunnelState != "connected" {
			continue
		}
		if d.ConnectionProperties.TransportType != "localNetwork" {
			continue
		}
		if d.HardwareProperties.UDID != "" {
			out = append(out, d.HardwareProperties.UDID)
		}
	}
	return out
}
