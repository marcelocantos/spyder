// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package paths computes central storage paths for spyder data.
// All persistent state lives under ~/.spyder/.
package paths

import (
	"os"
	"path/filepath"
)

// Base returns the spyder data directory (~/.spyder).
func Base() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".spyder"
	}
	return filepath.Join(home, ".spyder")
}

// SocketPath returns the Unix domain socket path for the daemon.
func SocketPath() string {
	return filepath.Join(Base(), "spyder.sock")
}

// InventoryPath returns the device inventory JSON file path.
func InventoryPath() string {
	return filepath.Join(Base(), "inventory.json")
}
