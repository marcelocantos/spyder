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

// ScreenshotsBase returns the default directory for screenshot files
// (~/.spyder/screenshots). Screenshot verbs write here when the caller
// asks for neither an explicit path nor an inline image (🎯T114).
func ScreenshotsBase() string {
	return filepath.Join(Base(), "screenshots")
}

// USBSpeedPath returns the USB link-speed ceiling store
// (~/.spyder/usb-speed.json). Highest observed kUSBDeviceSpeed per
// physical-device serial; ratchets up only (🎯T131.1).
func USBSpeedPath() string {
	return filepath.Join(Base(), "usb-speed.json")
}

// ListenAddrPath returns the last successful serve bind
// (~/.spyder/listen-addr). A supervisor restart without SPYDER_ADDR
// or --addr reuses this so LAN glasses keep working (🎯T103).
func ListenAddrPath() string {
	return filepath.Join(Base(), "listen-addr")
}

// ShipAuditBase returns the ship/fastlane audit root
// (~/.spyder/ship-audit). JSONL, daily markdown, and run logs /
// reflection stubs live here (🎯T133.6).
func ShipAuditBase() string {
	return filepath.Join(Base(), "ship-audit")
}
