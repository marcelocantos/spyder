// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package installdeadline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBudget_USB2LargeAppExceedsBaseInstall(t *testing.T) {
	const wantBytes = 1_500 * 1024 * 1024 // ~1.5 GiB Stock Cars debug
	tmp := t.TempDir()
	app := filepath.Join(tmp, "StockCars.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	chunk := filepath.Join(app, "Payload.bin")
	f, err := os.Create(chunk)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(wantBytes); err != nil {
		t.Fatal(err)
	}
	f.Close()

	d := Budget(app, "480 Mb/s", true)
	if d <= BaseInstall {
		t.Fatalf("budget %v should exceed base install %v for large USB2 deploy", d, BaseInstall)
	}
	if d < 12*time.Minute {
		t.Fatalf("budget %v too small for 1.5GiB @ USB2 pessimistic rate", d)
	}
	if d > MaxInstall {
		t.Fatalf("budget %v exceeds cap %v", d, MaxInstall)
	}
}

func TestBudget_SmallAppStaysAtBase(t *testing.T) {
	tmp := t.TempDir()
	app := filepath.Join(tmp, "Tiny.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "x"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Budget(app, "10 Gb/s", false) != BaseInstall {
		t.Fatalf("small install should keep base %v", BaseInstall)
	}
}

func TestArtifactBytes_IPA(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "x.ipa")
	if err := os.WriteFile(p, make([]byte, 1234), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := ArtifactBytes(p)
	if err != nil || n != 1234 {
		t.Fatalf("ArtifactBytes(%q) = %d, %v", p, n, err)
	}
}
