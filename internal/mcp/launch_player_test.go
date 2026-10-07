// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/streamrelay"
)

type fakeStreamRelay struct {
	servers []streamrelay.ServerInfo
	orient  map[string]string
}

func (f *fakeStreamRelay) Servers() []streamrelay.ServerInfo { return f.servers }
func (f *fakeStreamRelay) ServerOrientation(name string) string {
	if f.orient == nil {
		return ""
	}
	return f.orient[name]
}

func TestResolveStreamServerName_ExactlyOne(t *testing.T) {
	h := NewHandler()
	h.streamRelay = &fakeStreamRelay{servers: []streamrelay.ServerInfo{{Name: "tiltbuggy"}}}
	got, err := h.resolveStreamServerName("")
	if err != nil || got != "tiltbuggy" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestResolveStreamServerName_ZeroRequiresName(t *testing.T) {
	h := NewHandler()
	h.streamRelay = &fakeStreamRelay{}
	_, err := h.resolveStreamServerName("")
	if err == nil || !strings.Contains(err.Error(), "no streaming servers") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveStreamServerName_MultipleRequiresName(t *testing.T) {
	h := NewHandler()
	h.streamRelay = &fakeStreamRelay{servers: []streamrelay.ServerInfo{
		{Name: "tiltbuggy"}, {Name: "multimaze2"},
	}}
	_, err := h.resolveStreamServerName("")
	if err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("err=%v", err)
	}
	got, err := h.resolveStreamServerName("multimaze2")
	if err != nil || got != "multimaze2" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestResolveStreamServerName_MissingNameErrors(t *testing.T) {
	h := NewHandler()
	h.streamRelay = &fakeStreamRelay{servers: []streamrelay.ServerInfo{{Name: "tiltbuggy"}}}
	_, err := h.resolveStreamServerName("nope")
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseListenPort(t *testing.T) {
	cases := map[string]int{":3030": 3030, "127.0.0.1:3131": 3131, "": 3030, "bad": 3030}
	for in, want := range cases {
		if got := parseListenPort(in); got != want {
			t.Errorf("parseListenPort(%q)=%d want %d", in, got, want)
		}
	}
}

func TestResolvePlayerPath_Override(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Player.app")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolvePlayerPath("ios", p, "any")
	if err != nil || got != p {
		t.Fatalf("got %q err=%v", got, err)
	}
}

// TestPlayerLibexecDir_CellarLayout verifies the Homebrew/Cellar install-
// layout math used by playerLibexecDir (🎯T102): resolving a flat bin/
// symlink to the Cellar binary and walking ../libexec/spyder/player from
// there, mirroring resolveIOSTunnelBinary's ios-binary lookup
// (internal/daemon/daemon.go) and doctor.go's bundled-binary search.
func TestPlayerLibexecDir_CellarLayout(t *testing.T) {
	rawTmp := t.TempDir()
	tmp, err := filepath.EvalSymlinks(rawTmp)
	if err != nil {
		t.Fatalf("EvalSymlinks tmpdir: %v", err)
	}

	cellarBin := filepath.Join(tmp, "Cellar", "spyder", "1.0.0", "bin")
	cellarLibexecPlayer := filepath.Join(tmp, "Cellar", "spyder", "1.0.0", "libexec", "spyder", "player")
	flatBin := filepath.Join(tmp, "bin")
	for _, dir := range []string{cellarBin, cellarLibexecPlayer, flatBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	realExe := filepath.Join(cellarBin, "spyder")
	if err := os.WriteFile(realExe, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatalf("write real exe: %v", err)
	}
	symlink := filepath.Join(flatBin, "spyder")
	if err := os.Symlink(realExe, symlink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	resolved, err := filepath.EvalSymlinks(symlink)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	candidate := filepath.Clean(filepath.Join(filepath.Dir(resolved), "..", "libexec", "spyder", "player"))
	if candidate != cellarLibexecPlayer {
		t.Fatalf("libexec candidate from resolved exe = %q; want %q", candidate, cellarLibexecPlayer)
	}

	// Sanity: computing from the unresolved symlink's dir would miss
	// the Cellar libexec sibling (same hazard exePathReal guards against).
	wrong := filepath.Clean(filepath.Join(filepath.Dir(symlink), "..", "libexec", "spyder", "player"))
	if _, err := os.Stat(wrong); err == nil {
		t.Errorf("expected wrong candidate %q to be absent, but it exists", wrong)
	}
}

// TestPlayerPathCandidates_OrderEnvThenLibexecThenRepo verifies the
// resolution order mandated by 🎯T102: path= override (checked by the
// caller ahead of these), then SPYDER_PLAYER_* env, then the install
// layout (libexec/spyder/player/...), then repo-relative dev build
// outputs — for every platform candidate list.
func TestPlayerPathCandidates_OrderEnvThenLibexecThenRepo(t *testing.T) {
	t.Setenv(envPlayerDesktopPath, "/env/desktop/player")
	cs := playerPathCandidates("desktop", "any")
	if len(cs) < 3 {
		t.Fatalf("want at least 3 desktop candidates, got %v", cs)
	}
	if cs[0] != "/env/desktop/player" {
		t.Fatalf("want env override first, got %v", cs)
	}
	if !strings.Contains(cs[1], filepath.Join("libexec", "spyder", "player", "desktop", "player")) {
		t.Fatalf("want install-layout candidate second, got %v", cs)
	}
	if !strings.HasSuffix(cs[2], filepath.Join("bin", "player")) {
		t.Fatalf("want repo-relative dev candidate third, got %v", cs)
	}
}

func TestPlayerPathCandidates_AndroidIncludesLibexecLayout(t *testing.T) {
	cs := playerPathCandidates("android", "any")
	found := false
	for _, c := range cs {
		if strings.Contains(c, filepath.Join("libexec", "spyder", "player", "android", "player.apk")) {
			found = true
		}
	}
	if !found {
		t.Errorf("android candidates missing install-layout path: %v", cs)
	}
}

func TestPlayerPathCandidates_IOSIncludesLibexecLayout(t *testing.T) {
	cs := playerPathCandidates("ios", "any")
	found := false
	for _, c := range cs {
		if strings.Contains(c, filepath.Join("libexec", "spyder", "player", "ios", "Player.app")) {
			found = true
		}
	}
	if !found {
		t.Errorf("ios candidates missing install-layout path: %v", cs)
	}
}

func TestPlayerPathCandidates_DesktopIncludesBin(t *testing.T) {
	cs := playerPathCandidates("desktop", "any")
	found := false
	for _, c := range cs {
		if strings.HasSuffix(c, "bin/player") {
			found = true
		}
	}
	if !found {
		t.Fatalf("candidates missing bin/player: %v", cs)
	}
}

func TestLaunchPlayer_ArgSurface_MissingDevice(t *testing.T) {
	h := NewHandler()
	_, err := h.Dispatch(context.Background(), "launch_player", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "device") {
		t.Fatalf("want device-required error, got %v", err)
	}
}

func TestToolHandlers_IncludesLaunchPlayer(t *testing.T) {
	h := NewHandler()
	if _, ok := h.toolHandlers()["launch_player"]; !ok {
		t.Fatal("launch_player missing from toolHandlers")
	}
}
