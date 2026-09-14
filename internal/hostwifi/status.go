// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"fmt"
	"os/exec"
	"strings"
)

// Status is a point-in-time read of the macOS Wi-Fi hardware port.
type Status struct {
	Device    string // e.g. en0
	PowerOn   bool
	SSID      string // empty when not associated
	Connected bool   // power on and associated with an SSID
}

// ReadStatus discovers the Wi-Fi hardware port via networksetup and reads
// its power + association state. Returns an error when no Wi-Fi port
// exists (desktop Mac without Wi-Fi, Linux CI, …).
func ReadStatus() (Status, error) {
	device, err := wifiDevice()
	if err != nil {
		return Status{}, err
	}
	st := Status{Device: device}

	out, err := exec.Command("networksetup", "-getairportpower", device).CombinedOutput()
	if err != nil {
		return st, fmt.Errorf("networksetup -getairportpower %s: %w", device, err)
	}
	st.PowerOn = strings.Contains(strings.ToLower(string(out)), "on")

	out, err = exec.Command("networksetup", "-getairportnetwork", device).CombinedOutput()
	if err != nil {
		return st, fmt.Errorf("networksetup -getairportnetwork %s: %w", device, err)
	}
	text := strings.TrimSpace(string(out))
	const prefix = "Current Wi-Fi Network:"
	if strings.HasPrefix(text, prefix) {
		st.SSID = strings.TrimSpace(strings.TrimPrefix(text, prefix))
		st.Connected = st.PowerOn && st.SSID != ""
	}
	return st, nil
}

// wifiDevice returns the BSD device name for the Wi-Fi hardware port.
func wifiDevice() (string, error) {
	out, err := exec.Command("networksetup", "-listallhardwareports").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("networksetup -listallhardwareports: %w", err)
	}
	lines := strings.Split(string(out), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "Hardware Port:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "Hardware Port:"))
		if name != "Wi-Fi" && name != "AirPort" {
			continue
		}
		for j := i + 1; j < len(lines) && j <= i+3; j++ {
			devLine := strings.TrimSpace(lines[j])
			if strings.HasPrefix(devLine, "Device:") {
				dev := strings.TrimSpace(strings.TrimPrefix(devLine, "Device:"))
				if dev != "" {
					return dev, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no Wi-Fi hardware port found")
}
