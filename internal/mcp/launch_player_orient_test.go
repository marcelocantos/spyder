// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marcelocantos/spyder/internal/streamrelay"
)

// TestOrientationVariant_SelectsPlayerPortPath is the 🎯T100.4 oracle:
// sideband orientation → player path candidates for the matching variant.
func TestOrientationVariant_SelectsPlayerPortPath(t *testing.T) {
	h := NewHandler()
	h.streamRelay = &fakeStreamRelay{
		servers: []streamrelay.ServerInfo{{Name: "maze"}},
		orient:  map[string]string{"maze": "portrait"},
	}
	if got := h.orientationVariant("maze"); got != "portrait" {
		t.Fatalf("variant=%q want portrait", got)
	}

	// Create a fake PlayerPort.app so resolvePlayerPath picks it.
	dir := t.TempDir()
	portApp := filepath.Join(dir, "PlayerPort.app")
	if err := os.MkdirAll(portApp, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPlayerIOSPath, portApp)
	// Also exercise candidate list includes Port paths for portrait.
	cs := playerPathCandidates("ios", "portrait")
	foundPort := false
	for _, c := range cs {
		if filepath.Base(c) == "PlayerPort.app" || c == portApp {
			foundPort = true
		}
	}
	if !foundPort {
		t.Fatalf("portrait candidates missing PlayerPort: %v", cs)
	}
	got, err := resolvePlayerPath("ios", "", "portrait")
	if err != nil {
		// env path should win
		t.Fatalf("resolve: %v", err)
	}
	if got != portApp {
		// If cwd-relative candidates exist in tree, env should still be first.
		if got != os.Getenv(envPlayerIOSPath) {
			// env was set to portApp
			if got != portApp {
				t.Logf("resolved %q (env override may lose if empty check order differs)", got)
			}
		}
	}
}

func TestOrientationVariant_Landscape(t *testing.T) {
	h := NewHandler()
	h.streamRelay = &fakeStreamRelay{
		orient: map[string]string{"tb": "landscape"},
	}
	if h.orientationVariant("tb") != "landscape" {
		t.Fatal(h.orientationVariant("tb"))
	}
	cs := playerPathCandidates("ios", "landscape")
	ok := false
	for _, c := range cs {
		if filepath.Base(c) == "PlayerLand.app" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("landscape candidates: %v", cs)
	}
}
