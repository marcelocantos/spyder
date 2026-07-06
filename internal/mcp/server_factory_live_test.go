// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/inventory"
)

// TestServerFactory_Tiltbuggy_Live closes the 🎯T92 server-spawned medium with
// a REAL ge factory: spyder launches tiltbuggy in GE_FACTORY mode (which
// advertises spawn_instance), then app_spawn makes the factory fork an actual
// tiltbuggy game instance that dials spyder back as its own session — and the
// full monitor surface (app_tweak_list) works on that instance. No ged, no
// test-double: this is start-and-monitor on the server medium, end to end.
//
// Gated on SPYDER_GE_TILTBUGGY (launches real GUI processes); skips headless.
func TestServerFactory_Tiltbuggy_Live(t *testing.T) {
	bin := os.Getenv("SPYDER_GE_TILTBUGGY")
	if bin == "" {
		t.Skip("set SPYDER_GE_TILTBUGGY=<path to tiltbuggy> to run the T92 server-factory e2e")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	spyderDir := filepath.Join(home, ".spyder")
	if err := os.MkdirAll(spyderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []inventory.Entry{{
		Alias:          "tiltbuggy-factory",
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

	// Launch the factory (GE_FACTORY mode → advertises spawn_instance). Killing
	// the factory's process group on cleanup also reaps its spawned instances
	// (they inherit its group).
	launchArgs := map[string]any{
		"device":    "tiltbuggy-factory",
		"bundle_id": "com.squz.tiltbuggy.factory",
		"env":       map[string]any{"GE_FACTORY": "1"},
	}
	res, err := h.Dispatch("launch_app", launchArgs)
	if err != nil || res.IsError {
		t.Fatalf("launch factory: err=%v %s", err, callText(res))
	}
	t.Cleanup(func() { _, _ = h.Dispatch("terminate_app", launchArgs) })

	// Wait for the factory to connect.
	factoryID := waitForApp(t, mgr, "tiltbuggy-factory", 20*time.Second)
	if factoryID == "" {
		t.Fatal("factory did not connect over the app-channel")
	}

	// Ask the factory to spawn a game instance.
	spawnRes, err := h.Dispatch("app_spawn", map[string]any{"session_id": factoryID, "game": "tiltbuggy"})
	if err != nil || spawnRes.IsError {
		t.Fatalf("app_spawn: err=%v %s", err, callText(spawnRes))
	}
	var inst struct {
		SessionID string `json:"session_id"`
		AppName   string `json:"app_name"`
	}
	if err := json.Unmarshal([]byte(callText(spawnRes)), &inst); err != nil {
		t.Fatalf("decode app_spawn result: %v (%s)", err, callText(spawnRes))
	}
	if inst.SessionID == "" || inst.SessionID == factoryID {
		t.Fatalf("app_spawn did not return a distinct instance: %s", callText(spawnRes))
	}
	if inst.AppName != "tiltbuggy" {
		t.Fatalf("spawned instance app_name = %q, want tiltbuggy", inst.AppName)
	}

	// The monitor surface works on the spawned instance.
	tw, err := h.Dispatch("app_tweak_list", map[string]any{"session_id": inst.SessionID})
	if err != nil || tw.IsError || !strings.Contains(callText(tw), "camera.zoom") {
		t.Fatalf("app_tweak_list on spawned instance: err=%v %s", err, callText(tw))
	}
}

// waitForApp polls the manager for a session whose hello app_name matches, up
// to timeout, returning its session id ("" on timeout).
func waitForApp(t *testing.T, mgr *appchannel.Manager, appName string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, s := range mgr.Sessions() {
			if hi := s.HelloInfo(); hi != nil && hi.AppName == appName {
				return s.ID
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return ""
}
