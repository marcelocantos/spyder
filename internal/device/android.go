// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/network"
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
	for line := range strings.SplitSeq(string(out), "\n") {
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
	for line := range strings.SplitSeq(string(data), "\n") {
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
	for line := range strings.SplitSeq(string(out), "\n") {
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

// ListApps returns third-party packages via `adb shell pm list packages -3`.
// Only bundle ids are populated; per-app names/versions would need a
// dumpsys pass per package and are deferred until a use case lands.
func (a *AndroidAdapter) ListApps(id string) ([]AppInfo, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, fmt.Errorf("adb not found in PATH: %w", err)
	}
	out, stderr, err := runCapture("adb", "-s", id, "shell", "pm", "list", "packages", "-3")
	combined := string(stderr) + " " + string(out)
	if isAndroidDeviceNotConnected(combined) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("adb pm list packages: %w", err)
	}
	apps := []AppInfo{}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if pkg, ok := strings.CutPrefix(line, "package:"); ok {
			apps = append(apps, AppInfo{BundleID: pkg})
		}
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].BundleID < apps[j].BundleID })
	return apps, nil
}

// LaunchApp foregrounds an app via `adb shell monkey -p <pkg> -c LAUNCHER 1`.
func (a *AndroidAdapter) LaunchApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	out, stderr, err := runCapture("adb", "-s", id, "shell", "monkey", "-p", bundleID, "-c", "android.intent.category.LAUNCHER", "1")
	combined := string(stderr) + " " + string(out)
	if isAndroidDeviceNotConnected(combined) {
		return fmt.Errorf("device not connected: %s", id)
	}
	if strings.Contains(strings.ToLower(combined), "no activities found") ||
		strings.Contains(strings.ToLower(combined), "no packages found") {
		return fmt.Errorf("app not installed or has no launcher activity: %s", bundleID)
	}
	if err != nil {
		return fmt.Errorf("adb monkey: %v\n%s", err, truncate(string(stderr), 200))
	}
	return nil
}

// TerminateApp stops an app via `adb shell am force-stop <pkg>`.
func (a *AndroidAdapter) TerminateApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	_, stderr, err := runCapture("adb", "-s", id, "shell", "am", "force-stop", bundleID)
	if isAndroidDeviceNotConnected(string(stderr)) {
		return fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return fmt.Errorf("adb force-stop: %v\n%s", err, truncate(string(stderr), 200))
	}
	return nil
}

// ApplyNetwork shapes network conditions on an Android emulator via the
// adb telnet console "network speed" and "network delay" commands.
//
// This ONLY works on Android emulators (avd) — not on physical Android
// devices. adb console commands are tunnelled to the emulator's control
// socket; they are silently ignored or errored by real hardware because
// real hardware has no emulator control plane.
//
// Packet-loss profiles (lossy-<pct>) are not supported by the adb console
// protocol. The adb emu network commands control speed and delay only.
// If you need loss emulation on Android, use Android Studio's built-in
// network profiler, or apply it at the host OS/router level.
func (a *AndroidAdapter) ApplyNetwork(id string, p network.NetworkProfile) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}

	// Validate emulator accessibility: adb forward --list produces
	// output only for emulators (emulator-XXXX serials). Physical
	// devices will still accept the emu commands at the transport level,
	// but they'll silently fail. We proceed regardless and let the
	// emulator console error surface naturally.

	if p.IsOffline {
		// Offline: set speed to 0 in both directions. The adb console
		// accepts "0" as a valid speed value meaning "no traffic".
		if err := adbEmu(id, "network", "speed", "0"); err != nil {
			return fmt.Errorf("apply offline (speed 0): %w", err)
		}
		return nil
	}

	// Apply speed limit.
	if kw, ok := network.ADBSpeedClass(p); ok {
		if err := adbEmu(id, "network", "speed", kw); err != nil {
			return fmt.Errorf("apply speed %s: %w", kw, err)
		}
	} else if p.UploadKbps > 0 || p.DownloadKbps > 0 {
		// Numeric: "adb emu network speed <up> <down>"
		upStr := strconv.Itoa(p.UploadKbps)
		downStr := strconv.Itoa(p.DownloadKbps)
		if err := adbEmu(id, "network", "speed", upStr, downStr); err != nil {
			return fmt.Errorf("apply speed %s/%s kbps: %w", upStr, downStr, err)
		}
	} else {
		// Full speed (wifi / no limit).
		if err := adbEmu(id, "network", "speed", "full"); err != nil {
			return fmt.Errorf("apply full speed: %w", err)
		}
	}

	// Apply delay.
	if kw, ok := network.ADBDelayClass(p); ok {
		if err := adbEmu(id, "network", "delay", kw); err != nil {
			return fmt.Errorf("apply delay %s: %w", kw, err)
		}
	} else if p.DelayMs > 0 {
		if err := adbEmu(id, "network", "delay", strconv.Itoa(p.DelayMs)); err != nil {
			return fmt.Errorf("apply delay %d ms: %w", p.DelayMs, err)
		}
	} else {
		if err := adbEmu(id, "network", "delay", "none"); err != nil {
			return fmt.Errorf("apply delay none: %w", err)
		}
	}

	if p.LossPct > 0 {
		// Document the gap rather than silently ignoring it.
		return fmt.Errorf(
			"profile %q applied (speed/delay), but packet-loss (%d%%) is not supported "+
				"by the adb emulator console — use Android Studio's network profiler or a "+
				"host-level traffic shaper (tc/dummynet) for loss emulation",
			p.Name, p.LossPct,
		)
	}

	return nil
}

// ClearNetwork restores full-speed / no-delay conditions on an Android
// emulator by setting speed=full and delay=none.
func (a *AndroidAdapter) ClearNetwork(id string) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if err := adbEmu(id, "network", "speed", "full"); err != nil {
		return fmt.Errorf("clear network speed: %w", err)
	}
	if err := adbEmu(id, "network", "delay", "none"); err != nil {
		return fmt.Errorf("clear network delay: %w", err)
	}
	return nil
}

// adbEmu sends a command to the emulator console via `adb -s <id> emu
// <args...>`. The emulator console commands are available only on
// Android Virtual Devices (avd) — physical devices do not expose a
// console and will return an error.
func adbEmu(id string, args ...string) error {
	cmdArgs := append([]string{"-s", id, "emu"}, args...)
	out, stderr, err := runCapture("adb", cmdArgs...)
	if err != nil {
		combined := string(stderr) + " " + string(out)
		if isAndroidDeviceNotConnected(combined) {
			return fmt.Errorf("device not connected: %s", id)
		}
		return fmt.Errorf("%w: %s", err, truncate(string(stderr), 200))
	}
	// The emulator console echoes "OK" on success. Some versions echo
	// "KO: ..." on failure but still exit 0. Surface that as an error.
	outStr := strings.TrimSpace(string(out))
	if strings.HasPrefix(outStr, "KO:") {
		// Physical device or unsupported command.
		msg := strings.TrimPrefix(outStr, "KO:")
		msg = strings.TrimSpace(msg)
		if strings.Contains(strings.ToLower(msg), "not supported") ||
			strings.Contains(strings.ToLower(msg), "unknown command") {
			return fmt.Errorf(
				"adb emu network commands are only supported on Android emulators, "+
					"not physical devices (emulator replied: %s)", msg)
		}
		return fmt.Errorf("emulator console error: %s", msg)
	}
	return nil
}

// Screenshot captures a PNG via `adb shell screencap -p`. The subcommand
// writes the PNG to stdout, which we return unchanged.
func (a *AndroidAdapter) Screenshot(id string) ([]byte, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, fmt.Errorf("adb not found in PATH: %w", err)
	}
	out, stderr, err := runCapture("adb", "-s", id, "shell", "screencap", "-p")
	combined := string(stderr) + " " + string(out[:min(len(out), 512)])
	if isAndroidDeviceNotConnected(combined) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("adb screencap: %v\n%s", err, truncate(string(stderr), 200))
	}
	if len(out) == 0 {
		return nil, errors.New("adb screencap returned empty output")
	}
	return out, nil
}
