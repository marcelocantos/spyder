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

// InventoryPath returns the device inventory JSON file path.
func InventoryPath() string {
	return filepath.Join(Base(), "inventory.json")
}

// RunsBase returns the root directory for run-artefact bundles
// (~/.spyder/runs). Each reservation owns a subdirectory under this
// path containing a manifest.json plus captured screenshots, logs,
// recordings, and crash reports.
func RunsBase() string {
	return filepath.Join(Base(), "runs")
}

// BaselinesBase returns the root directory for the visual-regression
// baseline store (~/.spyder/baselines). Baselines are organised as
// <suite>/<variant>/<case>.{png,manifest.json}.
func BaselinesBase() string {
	return filepath.Join(Base(), "baselines")
}

// PoolConfigPath returns the path to the sim/emu pool catalogue
// (~/.spyder/pool.yaml).
func PoolConfigPath() string {
	return filepath.Join(Base(), "pool.yaml")
}

// PairingDir returns the directory for pigeon stream pairing state
// (~/.spyder/pairing) — identity key material and server-side
// PairingRecords for the spyder pairing host (🎯T93.1).
func PairingDir() string {
	return filepath.Join(Base(), "pairing")
}

// PairingIdentityPath is the long-term X25519/Ed25519 identity used
// as the pairing-ceremony acceptor for stream sessions.
func PairingIdentityPath() string {
	return filepath.Join(PairingDir(), "identity.json")
}

// PairingRecordsPath is the JSON map of peer instance ID →
// crypto.PairingRecord retained after successful ceremonies.
func PairingRecordsPath() string {
	return filepath.Join(PairingDir(), "pairings.json")
}
