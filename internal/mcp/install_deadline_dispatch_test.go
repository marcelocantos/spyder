// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/installdeadline"
)

func TestDispatchDeadline_LargeUSB2ArtifactExceedsClass(t *testing.T) {
	tmp := t.TempDir()
	app := filepath.Join(tmp, "Big.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(app, "blob"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1_500 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()

	args := map[string]any{"path": app, "device": "iPad"}
	d := dispatchDeadline(context.Background(), "deploy_app", args, "480 Mb/s")
	if d <= DeadlineInstall {
		t.Fatalf("dispatch deadline %v should exceed class %v", d, DeadlineInstall)
	}
	if d < 12*time.Minute {
		t.Fatalf("dispatch deadline %v too small for 1.5GiB USB2 deploy", d)
	}
}

func TestDispatchDeadline_HonorsLongerParent(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	d := dispatchDeadline(parent, "screenshot", nil, "")
	if d < 19*time.Minute {
		t.Fatalf("want parent deadline honored, got %v", d)
	}
}

func TestFormatTimeoutError_InstallInFlight(t *testing.T) {
	msg := formatTimeoutError("deploy_app", "iPad", 5*time.Minute, true)
	if !strings.Contains(msg, "tool timeout") || !strings.Contains(msg, "install transfer may still be running") {
		t.Fatalf("message: %s", msg)
	}
	msg = formatTimeoutError("deploy_app", "iPad", 5*time.Minute, false)
	if !strings.Contains(msg, "not in flight") {
		t.Fatalf("message: %s", msg)
	}
}

func TestDispatchDeadline_MatchesInstalldeadlinePackage(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "x.ipa")
	if err := os.WriteFile(p, make([]byte, 50*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"path": p}
	want := installdeadline.Budget(p, "5 Gb/s", true)
	got := dispatchDeadline(context.Background(), "deploy_app", args, "5 Gb/s")
	if got != want {
		t.Fatalf("dispatchDeadline=%v installdeadline.Budget=%v", got, want)
	}
}
