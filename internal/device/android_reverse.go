// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
package device

import (
	"fmt"
	"os/exec"
)

// ReverseTCP maps device localhost:port to host localhost:port over USB.
// adb reverse is idempotent for the same device/port pair.
func (a *AndroidAdapter) ReverseTCP(id string, port int) error {
	if id == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("adb reverse: invalid device or port")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found: %w", err)
	}
	spec := fmt.Sprintf("tcp:%d", port)
	out, stderr, err := androidAdb("-s", id, "reverse", spec, spec)
	if err != nil {
		return fmt.Errorf("adb reverse for %s: %w: %s", id, err, truncate(string(stderr)+string(out), 200))
	}
	return nil
}

// UnreverseTCP removes the device-side mapping when its listener stops.
func (a *AndroidAdapter) UnreverseTCP(id string, port int) error {
	if id == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("adb reverse: invalid device or port")
	}
	spec := fmt.Sprintf("tcp:%d", port)
	_, stderr, err := androidAdb("-s", id, "reverse", "--remove", spec)
	if err != nil {
		return fmt.Errorf("adb reverse --remove for %s: %w: %s", id, err, truncate(string(stderr), 200))
	}
	return nil
}
