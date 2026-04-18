// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// AndroidAdapter talks to Android devices via adb. Unlike iOS it does not
// need a KeepAwake companion app — Android offers a native "stay on while
// plugged in" developer setting. keepawake is therefore a gentle no-op
// that points the user at the OS setting.
type AndroidAdapter struct{}

// NewAndroidAdapter returns a new Android adapter.
func NewAndroidAdapter() *AndroidAdapter { return &AndroidAdapter{} }

// List returns connected Android devices via `adb devices -l`. Each device
// is queried for its model and OS version via `getprop`. Unauthorized or
// offline devices are included but their Model/OS may be empty.
func (a *AndroidAdapter) List() ([]Info, error) {
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, nil // adb not installed → treat as "no Android devices"
	}
	out, err := exec.Command("adb", "devices", "-l").Output()
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}
	var devices []Info
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		serial := fields[0]
		state := fields[1]
		info := Info{
			UUID:     serial,
			Platform: "android",
			Name:     serial,
		}
		for _, f := range fields[2:] {
			if k, v, ok := strings.Cut(f, ":"); ok {
				switch k {
				case "model":
					info.Model = v
				case "device":
					if info.Model == "" {
						info.Model = v
					}
				}
			}
		}
		if state == "device" {
			// getprop queries require the device to be fully online.
			if m := getprop(serial, "ro.product.model"); m != "" {
				info.Model = m
			}
			if v := getprop(serial, "ro.build.version.release"); v != "" {
				info.OS = "Android " + v
			}
		}
		devices = append(devices, info)
	}
	return devices, nil
}

func getprop(serial, key string) string {
	out, err := exec.Command("adb", "-s", serial, "shell", "getprop", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// State reports Android device state.
func (a *AndroidAdapter) State(id string) (State, error) {
	return State{}, errors.New("Android State not yet implemented (🎯T6.2)")
}

// LaunchKeepAwake is a no-op on Android: the OS provides a native
// "Stay awake while plugged in" developer setting. Returns nil to signal
// success; the tool handler surfaces a helpful message pointing the user
// at the setting.
func (a *AndroidAdapter) LaunchKeepAwake(id string) error {
	return nil
}
