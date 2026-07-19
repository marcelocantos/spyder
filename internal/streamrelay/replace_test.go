// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package streamrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestRelay_DualServerCatalogue lists two distinct server names at once (🎯T100.1).
func TestRelay_DualServerCatalogue(t *testing.T) {
	relay := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/server", relay.HandleServerSideband)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a, _, err := websocket.Dial(ctx, base+"/ws/server?name=tiltbuggy", nil)
	if err != nil {
		t.Fatalf("dial tiltbuggy: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(websocket.StatusNormalClosure, "") })
	b, _, err := websocket.Dial(ctx, base+"/ws/server?name=multimaze2", nil)
	if err != nil {
		t.Fatalf("dial multimaze2: %v", err)
	}
	t.Cleanup(func() { _ = b.Close(websocket.StatusNormalClosure, "") })

	if !waitFor(2*time.Second, func() bool { return len(relay.Servers()) == 2 }) {
		t.Fatalf("servers: got %v want 2", relay.Servers())
	}
	names := map[string]bool{}
	for _, s := range relay.Servers() {
		names[s.Name] = true
	}
	if !names["tiltbuggy"] || !names["multimaze2"] {
		t.Fatalf("catalogue names: %v", names)
	}
}

// TestRelay_SameNameReplace_ClosesOldSideband is the 🎯T100.1 oracle for
// rebuild-and-run without a live session: second connect replaces the holder
// and closes the previous sideband.
func TestRelay_SameNameReplace_ClosesOldSideband(t *testing.T) {
	relay := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/server", relay.HandleServerSideband)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	oldSB, _, err := websocket.Dial(ctx, base+"/ws/server?name=game1", nil)
	if err != nil {
		t.Fatalf("old server dial: %v", err)
	}
	if !waitFor(2*time.Second, func() bool { return len(relay.Servers()) == 1 }) {
		t.Fatal("old server not registered")
	}

	oldDone := make(chan error, 1)
	go func() {
		for {
			_, _, err := oldSB.Read(ctx)
			if err != nil {
				oldDone <- err
				return
			}
		}
	}()

	newSB, _, err := websocket.Dial(ctx, base+"/ws/server?name=game1", nil)
	if err != nil {
		t.Fatalf("new server dial: %v", err)
	}
	t.Cleanup(func() { _ = newSB.Close(websocket.StatusNormalClosure, "") })

	select {
	case err := <-oldDone:
		if err == nil {
			t.Fatal("old sideband read succeeded; want close after replace")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old sideband not closed after replace")
	}

	if !waitFor(2*time.Second, func() bool {
		servers := relay.Servers()
		return len(servers) == 1 && servers[0].Name == "game1"
	}) {
		t.Fatalf("servers after replace: %+v", relay.Servers())
	}
}

// TestRelay_SameNameReplace_EvictsSessions is the 🎯T100.1 oracle for
// rebuild-and-run with an active player: sessions for the old holder are
// closed when the name is replaced.
func TestRelay_SameNameReplace_EvictsSessions(t *testing.T) {
	relay := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/server", relay.HandleServerSideband)
	mux.HandleFunc("/ws/server/wire/", relay.HandleServerWire)
	mux.HandleFunc("/stream/player/", relay.HandlePlayerConnect)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	oldSB, _, err := websocket.Dial(ctx, base+"/ws/server?name=game1", nil)
	if err != nil {
		t.Fatalf("old server dial: %v", err)
	}
	if !waitFor(2*time.Second, func() bool { return len(relay.Servers()) == 1 }) {
		t.Fatal("old server not registered")
	}

	// Drain sideband in background so player_attached/detached don't block.
	go func() {
		for {
			if _, _, err := oldSB.Read(ctx); err != nil {
				return
			}
		}
	}()

	player, _, err := websocket.Dial(ctx, base+"/stream/player/game1", nil)
	if err != nil {
		t.Fatalf("player dial: %v", err)
	}
	t.Cleanup(func() { _ = player.Close(websocket.StatusNormalClosure, "") })

	// Wait for attach notification via session catalogue (server may race).
	if !waitFor(2*time.Second, func() bool { return len(relay.Sessions()) == 1 }) {
		t.Fatal("session not registered")
	}
	sid := relay.Sessions()[0].ID
	wire, _, err := websocket.Dial(ctx, base+"/ws/server/wire/"+sid, nil)
	if err != nil {
		t.Fatalf("wire dial: %v", err)
	}
	t.Cleanup(func() { _ = wire.Close(websocket.StatusNormalClosure, "") })

	newSB, _, err := websocket.Dial(ctx, base+"/ws/server?name=game1", nil)
	if err != nil {
		t.Fatalf("new server dial: %v", err)
	}
	t.Cleanup(func() { _ = newSB.Close(websocket.StatusNormalClosure, "") })

	if !waitFor(3*time.Second, func() bool { return len(relay.Sessions()) == 0 }) {
		t.Fatalf("sessions after replace: got %d want 0", len(relay.Sessions()))
	}

	// New attach goes to the new sideband only.
	go func() {
		for {
			if _, _, err := newSB.Read(ctx); err != nil {
				return
			}
		}
	}()
	player2, _, err := websocket.Dial(ctx, base+"/stream/player/game1", nil)
	if err != nil {
		t.Fatalf("player2 dial: %v", err)
	}
	t.Cleanup(func() { _ = player2.Close(websocket.StatusNormalClosure, "") })

	if !waitFor(2*time.Second, func() bool { return len(relay.Sessions()) == 1 }) {
		t.Fatal("new session not registered on replacement server")
	}
}
