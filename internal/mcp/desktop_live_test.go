// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/inventory"
)

// TestDesktopLaunch_Tiltbuggy_Live proves the full 🎯T85 loop through the mcp
// handler: a platform="desktop" inventory entry, resolved by launch_app, is
// started by the DesktopAdapter with SPYDER_APP_CHANNEL auto-injected, and the
// real tiltbuggy connects back over the app-channel — the same monitor surface
// as iOS/Android, reached with no device and no manual env wiring. This is the
// launch half (the monitor half was already proven this session); together
// they close T85's end-to-end acceptance.
//
// Gated on SPYDER_GE_TILTBUGGY (launches a real GUI app); skips headless.
func TestDesktopLaunch_Tiltbuggy_Live(t *testing.T) {
	bin := os.Getenv("SPYDER_GE_TILTBUGGY")
	if bin == "" {
		t.Skip("set SPYDER_GE_TILTBUGGY=<path to tiltbuggy> to run the T85 desktop launch e2e")
	}

	// Hermetic ~/.spyder with a single desktop entry for tiltbuggy.
	home := t.TempDir()
	t.Setenv("HOME", home)
	spyderDir := filepath.Join(home, ".spyder")
	if err := os.MkdirAll(spyderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []inventory.Entry{{
		Alias:          "tiltbuggy-desktop",
		Platform:       "desktop",
		ExecutablePath: bin,
	}}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(spyderDir, "inventory.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := appchannel.NewManager()
	t.Cleanup(mgr.Close)
	h := NewHandler(WithAppChannel(mgr))

	const bundle = "com.squz.tiltbuggy"
	launchArgs := map[string]any{"device": "tiltbuggy-desktop", "bundle_id": bundle}
	res, err := h.Dispatch(context.Background(), "launch_app", launchArgs)
	if err != nil {
		t.Fatalf("launch_app dispatch: %v", err)
	}
	if res.IsError {
		t.Fatalf("launch_app failed: %s", callText(res))
	}
	t.Cleanup(func() { _, _ = h.Dispatch(context.Background(), "terminate_app", launchArgs) })

	// The app must dial back in over the app-channel.
	var sessionID string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range mgr.Sessions() {
			if hi := s.HelloInfo(); hi != nil && hi.AppName == "tiltbuggy" {
				sessionID = s.ID
			}
		}
		if sessionID != "" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if sessionID == "" {
		t.Fatal("tiltbuggy did not connect over the app-channel after desktop launch_app")
	}

	// And the monitor surface works against it (sanity: tweaks resolve).
	tw, err := h.Dispatch(context.Background(), "app_tweak_list", map[string]any{"session_id": sessionID})
	if err != nil {
		t.Fatalf("app_tweak_list dispatch: %v", err)
	}
	if tw.IsError || !strings.Contains(callText(tw), "camera.zoom") {
		t.Fatalf("app_tweak_list over desktop-launched app unexpected: %s", callText(tw))
	}
}

// TestDesktopDevicesList verifies `devices(platform="desktop")` surfaces
// platform=desktop inventory entries with their executable_path (🎯T85
// acceptance clause 2). Headless — no process launched.
func TestDesktopDevicesList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spyderDir := filepath.Join(home, ".spyder")
	if err := os.MkdirAll(spyderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []inventory.Entry{{
		Alias:          "tiltbuggy-desktop",
		Platform:       "desktop",
		ExecutablePath: "/opt/games/tiltbuggy",
	}}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(spyderDir, "inventory.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler()
	res, err := h.Dispatch(context.Background(), "devices", map[string]any{"platform": "desktop"})
	if err != nil || res.IsError {
		t.Fatalf("devices(platform=desktop): err=%v %s", err, callText(res))
	}
	txt := callText(res)
	if !strings.Contains(txt, "tiltbuggy-desktop") || !strings.Contains(txt, "/opt/games/tiltbuggy") {
		t.Fatalf("devices(platform=desktop) missing the desktop entry: %s", txt)
	}
}

// callText concatenates the text content blocks of a tool result.
func callText(res *mcpgo.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
