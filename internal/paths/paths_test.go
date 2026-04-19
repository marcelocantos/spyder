// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBase_HomeDir(t *testing.T) {
	t.Setenv("HOME", "/custom/home")
	got := Base()
	want := filepath.Join("/custom/home", ".spyder")
	if got != want {
		t.Errorf("Base() = %q; want %q", got, want)
	}
}

func TestInventoryPath(t *testing.T) {
	t.Setenv("HOME", "/custom/home")
	got := InventoryPath()
	if !strings.HasSuffix(got, filepath.Join(".spyder", "inventory.json")) {
		t.Errorf("InventoryPath() = %q; want …/.spyder/inventory.json", got)
	}
	if !strings.HasPrefix(got, "/custom/home") {
		t.Errorf("InventoryPath() = %q; expected /custom/home prefix", got)
	}
}

func TestRunsBase(t *testing.T) {
	t.Setenv("HOME", "/custom/home")
	got := RunsBase()
	want := filepath.Join("/custom/home", ".spyder", "runs")
	if got != want {
		t.Errorf("RunsBase() = %q; want %q", got, want)
	}
}
