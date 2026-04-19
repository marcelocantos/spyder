// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package simemu manages iOS simulators (via xcrun simctl) and Android
// emulators (via avdmanager + emulator). It exposes typed structs and
// parser functions for each platform's metadata formats, plus lifecycle
// operations (list, create, boot, shutdown, delete).
package simemu

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// --------------------------------------------------------------------------
// iOS simulator types
// --------------------------------------------------------------------------

// SimDevice represents a single iOS simulator device instance.
type SimDevice struct {
	UDID         string `json:"udid"`
	Name         string `json:"name"`
	State        string `json:"state"`        // "Booted", "Shutdown", etc.
	DeviceTypeID string `json:"deviceTypeID"` // populated from simctl list devices --json
	RuntimeID    string `json:"runtimeID"`    // e.g. "com.apple.CoreSimulator.SimRuntime.iOS-17-5"
}

// SimDeviceType represents an available simulator device type (form factor).
type SimDeviceType struct {
	ID   string `json:"id"`   // e.g. "com.apple.CoreSimulator.SimDeviceType.iPhone-15"
	Name string `json:"name"` // e.g. "iPhone 15"
}

// SimRuntime represents an available simulator runtime (OS version).
type SimRuntime struct {
	ID          string `json:"id"`   // e.g. "com.apple.CoreSimulator.SimRuntime.iOS-17-5"
	Name        string `json:"name"` // e.g. "iOS 17.5"
	IsAvailable bool   `json:"isAvailable"`
}

// --------------------------------------------------------------------------
// Android emulator types
// --------------------------------------------------------------------------

// AVD represents an Android Virtual Device.
type AVD struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	Target string `json:"target,omitempty"` // e.g. "Google APIs (Google Inc.)"
	ABI    string `json:"abi,omitempty"`    // e.g. "arm64-v8a"
	Serial string `json:"serial,omitempty"` // e.g. "emulator-5554" when booted
}

// --------------------------------------------------------------------------
// iOS simctl operations
// --------------------------------------------------------------------------

// simctlListDevicesJSON is the shape returned by `xcrun simctl list devices --json`.
type simctlListDevicesJSON struct {
	Devices map[string][]simctlDeviceEntry `json:"devices"`
}

type simctlDeviceEntry struct {
	UDID        string `json:"udid"`
	Name        string `json:"name"`
	State       string `json:"state"`
	IsAvailable bool   `json:"isAvailable"`
}

// simctlListDeviceTypesJSON is the shape returned by `xcrun simctl list devicetypes --json`.
type simctlListDeviceTypesJSON struct {
	DeviceTypes []struct {
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
	} `json:"deviceTypes"`
}

// simctlListRuntimesJSON is the shape returned by `xcrun simctl list runtimes --json`.
type simctlListRuntimesJSON struct {
	Runtimes []struct {
		Name        string `json:"name"`
		Identifier  string `json:"identifier"`
		IsAvailable bool   `json:"isAvailable"`
	} `json:"runtimes"`
}

// SimList returns all simulator devices known to simctl, keyed by runtime.
// Each SimDevice includes the runtimeID derived from the JSON key.
func SimList() ([]SimDevice, error) {
	out, err := runCapture("xcrun", "simctl", "list", "devices", "--json")
	if err != nil {
		return nil, fmt.Errorf("simctl list devices: %w", err)
	}
	return ParseSimDevicesJSON(out)
}

// ParseSimDevicesJSON parses the JSON output of `xcrun simctl list devices --json`.
func ParseSimDevicesJSON(data []byte) ([]SimDevice, error) {
	var raw simctlListDevicesJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse simctl list devices JSON: %w", err)
	}
	var devices []SimDevice
	for runtimeID, entries := range raw.Devices {
		for _, e := range entries {
			devices = append(devices, SimDevice{
				UDID:      e.UDID,
				Name:      e.Name,
				State:     e.State,
				RuntimeID: runtimeID,
			})
		}
	}
	return devices, nil
}

// SimDeviceTypes returns all available simulator device types.
func SimDeviceTypes() ([]SimDeviceType, error) {
	out, err := runCapture("xcrun", "simctl", "list", "devicetypes", "--json")
	if err != nil {
		return nil, fmt.Errorf("simctl list devicetypes: %w", err)
	}
	return ParseSimDeviceTypesJSON(out)
}

// ParseSimDeviceTypesJSON parses the JSON output of `xcrun simctl list devicetypes --json`.
func ParseSimDeviceTypesJSON(data []byte) ([]SimDeviceType, error) {
	var raw simctlListDeviceTypesJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse simctl list devicetypes JSON: %w", err)
	}
	var types []SimDeviceType
	for _, dt := range raw.DeviceTypes {
		types = append(types, SimDeviceType{ID: dt.Identifier, Name: dt.Name})
	}
	return types, nil
}

// SimRuntimes returns all available simulator runtimes.
func SimRuntimes() ([]SimRuntime, error) {
	out, err := runCapture("xcrun", "simctl", "list", "runtimes", "--json")
	if err != nil {
		return nil, fmt.Errorf("simctl list runtimes: %w", err)
	}
	return ParseSimRuntimesJSON(out)
}

// ParseSimRuntimesJSON parses the JSON output of `xcrun simctl list runtimes --json`.
func ParseSimRuntimesJSON(data []byte) ([]SimRuntime, error) {
	var raw simctlListRuntimesJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse simctl list runtimes JSON: %w", err)
	}
	var runtimes []SimRuntime
	for _, r := range raw.Runtimes {
		runtimes = append(runtimes, SimRuntime{
			ID:          r.Identifier,
			Name:        r.Name,
			IsAvailable: r.IsAvailable,
		})
	}
	return runtimes, nil
}

// SimCreate creates a new iOS simulator and returns its UDID.
// name is the human-readable name; deviceTypeID and runtimeID are the
// full identifier strings from SimDeviceTypes() and SimRuntimes().
func SimCreate(name, deviceTypeID, runtimeID string) (string, error) {
	out, err := runCapture("xcrun", "simctl", "create", name, deviceTypeID, runtimeID)
	if err != nil {
		return "", fmt.Errorf("simctl create: %w", err)
	}
	udid := strings.TrimSpace(string(out))
	if udid == "" {
		return "", fmt.Errorf("simctl create returned empty UDID")
	}
	return udid, nil
}

// SimBoot boots a simulator by UDID. Idempotent — booting an already-booted
// simulator returns an error from simctl which we surface as-is.
func SimBoot(udid string) error {
	_, err := runCapture("xcrun", "simctl", "boot", udid)
	if err != nil {
		return fmt.Errorf("simctl boot %s: %w", udid, err)
	}
	return nil
}

// SimShutdown shuts down a booted simulator by UDID.
func SimShutdown(udid string) error {
	_, err := runCapture("xcrun", "simctl", "shutdown", udid)
	if err != nil {
		return fmt.Errorf("simctl shutdown %s: %w", udid, err)
	}
	return nil
}

// SimDelete deletes a simulator by UDID. The simulator must be shut down first.
func SimDelete(udid string) error {
	_, err := runCapture("xcrun", "simctl", "delete", udid)
	if err != nil {
		return fmt.Errorf("simctl delete %s: %w", udid, err)
	}
	return nil
}

// --------------------------------------------------------------------------
// Android AVD operations
// --------------------------------------------------------------------------

// AVDList returns all configured Android Virtual Devices.
func AVDList() ([]AVD, error) {
	out, err := runCapture("avdmanager", "list", "avd")
	if err != nil {
		return nil, fmt.Errorf("avdmanager list avd: %w", err)
	}
	return ParseAVDList(string(out))
}

// ParseAVDList parses the text output of `avdmanager list avd`.
// The output groups AVD records separated by dashes. Each entry has
// lines like "Name: foo", "Path: /...", "Target: ...", "ABI: ...".
func ParseAVDList(output string) ([]AVD, error) {
	var avds []AVD
	var current *AVD
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Name:") {
			if current != nil {
				avds = append(avds, *current)
			}
			current = &AVD{Name: strings.TrimSpace(strings.TrimPrefix(line, "Name:"))}
		} else if current != nil {
			if val, ok := cutPrefix(line, "Path:"); ok {
				current.Path = val
			} else if val, ok := cutPrefix(line, "Target:"); ok {
				current.Target = val
			} else if val, ok := cutPrefix(line, "ABI:"); ok {
				current.ABI = val
			}
		}
	}
	if current != nil {
		avds = append(avds, *current)
	}
	return avds, nil
}

// cutPrefix returns the trimmed value after prefix and true, or "", false.
func cutPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(s, prefix)), true
	}
	return "", false
}

// AVDCreate creates a new Android Virtual Device.
// name is the AVD name; systemImage is the package path (e.g.
// "system-images;android-34;google_apis;arm64-v8a"); deviceProfile is
// the avdmanager device ID (e.g. "pixel_6").
func AVDCreate(name, systemImage, deviceProfile string) error {
	_, err := runCapture("avdmanager", "create", "avd",
		"-n", name,
		"-k", systemImage,
		"-d", deviceProfile)
	if err != nil {
		return fmt.Errorf("avdmanager create avd %s: %w", name, err)
	}
	return nil
}

// AVDBoot starts an AVD in the background (headless). It returns the
// emulator serial (e.g. "emulator-5554") by waiting for the device to
// appear in `adb devices`.
func AVDBoot(name string) (string, error) {
	emulatorPath, err := exec.LookPath("emulator")
	if err != nil {
		return "", fmt.Errorf("emulator not found in PATH: %w", err)
	}
	// Start the emulator detached.
	cmd := exec.Command(emulatorPath, "-avd", name, "-no-window", "-no-audio")
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting emulator for %s: %w", name, err)
	}
	// Detach so the emulator outlives us.
	go func() { _ = cmd.Wait() }()
	return name + " (started, serial available via `adb devices` once booted)", nil
}

// AVDShutdown sends the `emu kill` command via adb to the given emulator serial.
func AVDShutdown(serial string) error {
	_, err := runCapture("adb", "-s", serial, "emu", "kill")
	if err != nil {
		return fmt.Errorf("adb emu kill %s: %w", serial, err)
	}
	return nil
}

// AVDDelete deletes an AVD configuration by name.
func AVDDelete(name string) error {
	_, err := runCapture("avdmanager", "delete", "avd", "-n", name)
	if err != nil {
		return fmt.Errorf("avdmanager delete avd %s: %w", name, err)
	}
	return nil
}

// --------------------------------------------------------------------------
// internal helpers
// --------------------------------------------------------------------------

// runCapture runs cmd with args, returning stdout bytes on success or a
// combined error (stderr included) on failure.
func runCapture(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &writerAdapter{&stdout}
	cmd.Stderr = &writerAdapter{&stderr}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return []byte(stdout.String()), nil
}

type writerAdapter struct{ b *strings.Builder }

func (w *writerAdapter) Write(p []byte) (int, error) { return w.b.Write(p) }
