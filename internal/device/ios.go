// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// KeepAwakeBundleID is the bundle identifier of the ios/KeepAwake companion
// app. Any iOS device that should hold its screen awake must have this app
// installed; LaunchKeepAwake foregrounds it via devicectl.
const KeepAwakeBundleID = "com.marcelocantos.spyder.KeepAwake"

// IOSAdapter talks to iOS devices via pymobiledevice3 and devicectl.
type IOSAdapter struct{}

// NewIOSAdapter returns a new iOS adapter.
func NewIOSAdapter() *IOSAdapter { return &IOSAdapter{} }

// List returns connected iOS devices.
func (a *IOSAdapter) List() ([]Info, error) {
	// TODO: shell out to `pymobiledevice3 usbmux list --usbmux --no-color`
	// and parse the JSON array. Fall back to `xcrun xctrace list devices`
	// if pymobiledevice3 is unavailable.
	return nil, errors.New("iOS List not yet implemented")
}

// State reports iOS device state.
func (a *IOSAdapter) State(id string) (State, error) {
	// TODO: combine `pymobiledevice3 diagnostics battery` (battery,
	// charging), `diagnostics ioreg` (thermal), and springboard queries
	// (foreground app).
	return State{}, errors.New("iOS State not yet implemented")
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
