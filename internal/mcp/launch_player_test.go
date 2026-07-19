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
