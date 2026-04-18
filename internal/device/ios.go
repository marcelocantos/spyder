// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// KeepAwakeBundleID is the bundle identifier of the ios/KeepAwake companion
// app. Any iOS device that should hold its screen awake must have this app
// installed; LaunchKeepAwake foregrounds it via devicectl.
const KeepAwakeBundleID = "com.marcelocantos.spyder.KeepAwake"

// stateTTL bounds how often we re-query a device. Tools called in quick
// succession (e.g. from an agent reasoning loop) share a snapshot so the
// device isn't hammered.
const stateTTL = 2 * time.Second

// IOSAdapter talks to iOS devices via pymobiledevice3 and devicectl.
type IOSAdapter struct {
	mu    sync.Mutex
	cache map[string]cachedState
}

type cachedState struct {
	state State
	at    time.Time
}

// NewIOSAdapter returns a new iOS adapter.
func NewIOSAdapter() *IOSAdapter { return &IOSAdapter{cache: map[string]cachedState{}} }

// List returns connected iOS devices.
func (a *IOSAdapter) List() ([]Info, error) {
	// TODO: shell out to `pymobiledevice3 usbmux list --usbmux --no-color`
	// and parse the JSON array. Fall back to `xcrun xctrace list devices`
	// if pymobiledevice3 is unavailable.
	return nil, errors.New("iOS List not yet implemented")
}

// State reports iOS device state. Shells out to pymobiledevice3 for
// battery/charging data; thermal state and foreground app are currently
// returned as notes (MobileGestalt deprecated on iOS 17.4+; foreground
// app requires additional tooling). Results are cached per-device for
// stateTTL to avoid hammering the device.
func (a *IOSAdapter) State(id string) (State, error) {
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

	if _, err := exec.LookPath("pymobiledevice3"); err != nil {
		return State{}, fmt.Errorf("pymobiledevice3 not found in PATH: %w", err)
	}

	var state State

	batteryOut, batteryStderr, batteryErr := runCapture("pymobiledevice3", "diagnostics", "battery", "single", "--udid", id)
	// pymobiledevice3 sometimes exits 0 even when the device isn't
	// connected, logging the failure to stderr. Check stderr regardless
	// of exit code.
	batteryStderrStr := string(batteryStderr)
	if isDeviceNotConnected(batteryStderrStr) {
		return State{}, fmt.Errorf("device not connected: %s", id)
	}
	if batteryErr != nil || len(bytes.TrimSpace(batteryOut)) == 0 {
		state.Notes = append(state.Notes, fmt.Sprintf("battery data unavailable: %s", truncate(batteryStderrStr, 160)))
	} else if level, charging, err := parseBattery(batteryOut); err != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("battery parse error: %v", err))
	} else {
		state.BatteryLevel = &level
		state.Charging = &charging
	}

	state.Notes = append(state.Notes,
		"thermal state unavailable on iOS 17.4+ (MobileGestalt deprecated)",
		"foreground app detection pending — not yet wired",
	)

	a.mu.Lock()
	a.cache[id] = cachedState{state: state, at: time.Now()}
	a.mu.Unlock()

	return state, nil
}

func parseBattery(data []byte) (level int, charging bool, err error) {
	var b struct {
		AppleRawCurrentCapacity   int  `json:"AppleRawCurrentCapacity"`
		AppleRawMaxCapacity       int  `json:"AppleRawMaxCapacity"`
		AppleRawExternalConnected bool `json:"AppleRawExternalConnected"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return 0, false, fmt.Errorf("battery JSON: %w", err)
	}
	if b.AppleRawMaxCapacity == 0 {
		return 0, false, errors.New("AppleRawMaxCapacity is zero")
	}
	level = int(float64(b.AppleRawCurrentCapacity) / float64(b.AppleRawMaxCapacity) * 100)
	charging = b.AppleRawExternalConnected
	return level, charging, nil
}

// runCapture runs a command and returns stdout, stderr, and the run error.
// Unlike exec.Cmd.Output/CombinedOutput, this keeps stdout and stderr
// separate so callers can parse JSON from stdout while still inspecting
// human-readable diagnostics from stderr (pymobiledevice3 sometimes logs
// errors to stderr with exit code 0).
func runCapture(name string, args ...string) (stdout, stderr []byte, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

func isDeviceNotConnected(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "nodeviceconnectederror") ||
		strings.Contains(s, "device not found") ||
		strings.Contains(s, "no devices connected")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Screenshot captures a PNG via `pymobiledevice3 developer dvt screenshot`.
// Requires a live tunneld session on iOS 17+; callers should gate on
// tunneld health before invoking.
func (a *IOSAdapter) Screenshot(id string) ([]byte, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	tmp, err := os.MkdirTemp("", "spyder-screenshot-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "screen.png")

	_, stderr, runErr := runCapture("pymobiledevice3", "developer", "dvt", "screenshot", out, "--udid", id)
	stderrStr := string(stderr)
	// pymobiledevice3 sometimes exits 0 without writing the file
	// (e.g. unknown UDID logged as ERROR to stderr). Inspect stderr
	// regardless of exit code.
	if isDeviceNotConnected(stderrStr) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if runErr != nil {
		return nil, fmt.Errorf("pymobiledevice3 dvt screenshot: %v\n%s", runErr, truncate(stderrStr, 240))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		// File wasn't created despite exit 0 — surface stderr so the
		// caller can see why (ERROR lines typically explain).
		return nil, fmt.Errorf("capture did not produce a file: %s", truncate(stderrStr, 240))
	}
	return data, nil
}

// LaunchKeepAwake brings the KeepAwake app to foreground via devicectl.
// The id may be the hardware UDID, CoreDevice UUID, device name, or any
// other identifier `devicectl --device` accepts.
func (a *IOSAdapter) LaunchKeepAwake(id string) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	cmd := exec.Command("xcrun", "devicectl", "device", "process", "launch",
		"--device", id,
		KeepAwakeBundleID,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("devicectl launch: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
