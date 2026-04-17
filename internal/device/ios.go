// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import "errors"

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

// LaunchKeepAwake brings the KeepAwake app to foreground.
func (a *IOSAdapter) LaunchKeepAwake(id string) error {
	// TODO: `devicectl device process launch --device <id> com.marcelocantos.spyder.KeepAwake`
	// once the companion app is built and installable.
	return errors.New("iOS LaunchKeepAwake not yet implemented — companion app pending")
}
