// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AndroidAdapter talks to Android devices via adb. Unlike iOS it does not
// need a KeepAwake companion app — Android offers a native "stay on while
// plugged in" developer setting. keepawake is therefore a gentle no-op
// that points the user at the OS setting.
type AndroidAdapter struct {
	mu    sync.Mutex
	cache map[string]cachedState
}

// NewAndroidAdapter returns a new Android adapter.
func NewAndroidAdapter() *AndroidAdapter {
	return &AndroidAdapter{cache: map[string]cachedState{}}
}

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

// State reports Android device state via adb dumpsys. Battery level,
// charging status, and foreground app come from `dumpsys battery` and
// `dumpsys activity activities`. Results are cached per-device for
// stateTTL (shared with the iOS adapter).
func (a *AndroidAdapter) State(id string) (State, error) {
	if id == "" {
		return State{}, errors.New("device identifier is empty")
	}

	a.mu.Lock()
	if c, ok := a.cache[id]; ok && time.Since(c.at) < stateTTL {
		s := c.state
		a.mu.Unlock()
		return s, nil
	}
	a.mu.Unlock()

	if _, err := exec.LookPath("adb"); err != nil {
		return State{}, fmt.Errorf("adb not found in PATH: %w", err)
	}

	var state State

	battOut, battStderr, battErr := runCapture("adb", "-s", id, "shell", "dumpsys", "battery")
	combined := string(battStderr) + " " + string(battOut)
	if isAndroidDeviceNotConnected(combined) {
		return State{}, fmt.Errorf("device not connected: %s", id)
	}
	if battErr != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("battery data unavailable: %s", truncate(string(battStderr), 160)))
	} else if level, charging, err := parseAndroidBattery(battOut); err != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("battery parse error: %v", err))
	} else {
		state.BatteryLevel = &level
		state.Charging = &charging
	}

	if fg, err := androidForegroundApp(id); err != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("foreground app unavailable: %v", err))
	} else {
		state.ForegroundApp = fg
	}

	state.Notes = append(state.Notes, "thermal state not yet wired on Android")

	a.mu.Lock()
	a.cache[id] = cachedState{state: state, at: time.Now()}
	a.mu.Unlock()

	return state, nil
}

// parseAndroidBattery extracts level and charging status from
// `dumpsys battery` output. Charging is true when any of AC/USB/
// Wireless/Dock is powered.
func parseAndroidBattery(data []byte) (level int, charging bool, err error) {
	levelFound := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "level":
				if n, err := strconv.Atoi(v); err == nil {
					level = n
					levelFound = true
				}
			case "AC powered", "USB powered", "Wireless powered", "Dock powered":
				if v == "true" {
					charging = true
				}
			}
		}
	}
	if !levelFound {
		return 0, false, errors.New("no 'level' field in dumpsys battery output")
	}
	return level, charging, nil
}

// androidForegroundApp returns the foreground activity's package id
// parsed from `dumpsys activity activities`.
var fgActivityRE = regexp.MustCompile(`ActivityRecord\{[^}]*\s([a-zA-Z0-9_.]+)/[^}]+\}`)

func androidForegroundApp(id string) (string, error) {
	out, stderr, err := runCapture("adb", "-s", id, "shell", "dumpsys", "activity", "activities")
	if err != nil {
		return "", fmt.Errorf("%s", truncate(string(stderr), 120))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "mFocusedApp=") && !strings.Contains(line, "mResumedActivity") {
			continue
		}
		if m := fgActivityRE.FindStringSubmatch(line); len(m) >= 2 {
			return m[1], nil
		}
	}
	return "", errors.New("no focused or resumed activity found")
}

// isAndroidDeviceNotConnected recognises adb's "not found"/"offline"/
// "unauthorized" error messages from combined stdout+stderr.
func isAndroidDeviceNotConnected(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "device offline") ||
		strings.Contains(l, "device unauthorized") ||
		strings.Contains(l, "no devices/emulators found") ||
		(strings.Contains(l, "device") && strings.Contains(l, "not found"))
}

// LaunchKeepAwake is a no-op on Android: the OS provides a native
// "Stay awake while plugged in" developer setting. Returns nil to signal
// success; the tool handler surfaces a helpful message pointing the user
// at the setting.
func (a *AndroidAdapter) LaunchKeepAwake(id string) error {
	return nil
}
