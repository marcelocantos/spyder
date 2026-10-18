// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathsDefaultAndDataDirectoryOverride(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	originalHome := os.Getenv("HOME")
	locations := []struct {
		name   string
		get    func() string
		suffix string
	}{
		{"base", Base, ""}, {"inventory", InventoryPath, "inventory.json"},
		{"runs", RunsBase, "runs"}, {"baselines", BaselinesBase, "baselines"},
		{"pool", PoolConfigPath, "pool.yaml"}, {"screenshots", ScreenshotsBase, "screenshots"},
		{"usb", USBSpeedPath, "usb-speed.json"}, {"listen", ListenAddrPath, "listen-addr"},
		{"audit", ShipAuditBase, "ship-audit"}, {"verify", VerifyDir, "verify"}, {"battery", BatteryDir, "battery"},
		{"captures", CapturesBase, "captures"},
	}
	t.Run("default", func(t *testing.T) {
		t.Setenv(DataDirEnv, "")
		for _, loc := range locations {
			if got, want := loc.get(), filepath.Join(home, ".spyder", loc.suffix); got != want {
				t.Errorf("%s: got %q want %q", loc.name, got, want)
			}
		}
	})
	t.Run("isolated", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(DataDirEnv, root+string(filepath.Separator))
		for _, loc := range locations {
			if got, want := loc.get(), filepath.Join(root, loc.suffix); got != want {
				t.Errorf("%s: got %q want %q", loc.name, got, want)
			}
		}
		if got := os.Getenv("HOME"); got != originalHome {
			t.Fatalf("data override changed home: %q", got)
		}
	})
}
