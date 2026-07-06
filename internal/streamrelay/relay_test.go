// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package streamrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestRelay_PipesFramesAndInput is the 🎯T91.4 oracle: a test-double server and
// player, no ge. A player attaching triggers player_attached on the server's
// control socket; the server opens the matching wire; a frame sent down the
// wire reaches the player verbatim, and input sent by the player reaches the
// wire verbatim. Proves the relay's pairing + byte-piping.
func TestRelay_PipesFramesAndInput(t *testing.T) {
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

	// Server control socket.
	sideband, _, err := websocket.Dial(ctx, base+"/ws/server?name=game1", nil)
	if err != nil {
		t.Fatalf("server dial: %v", err)
	}
	t.Cleanup(func() { _ = sideband.Close(websocket.StatusNormalClosure, "") })
	// The relay registers the server on Accept; a tiny wait avoids racing the
	// player dial against registration.
	if !waitFor(2*time.Second, func() bool { return len(relay.Servers()) == 1 }) {
		t.Fatal("server not registered")
	}

	// Player attaches.
	player, _, err := websocket.Dial(ctx, base+"/stream/player/game1", nil)
	if err != nil {
		t.Fatalf("player dial: %v", err)
	}
	t.Cleanup(func() { _ = player.Close(websocket.StatusNormalClosure, "") })

	// Server reads player_attached and learns the session id.
	_, msg, err := sideband.Read(ctx)
	if err != nil {
		t.Fatalf("read player_attached: %v", err)
	}
	var attach struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(msg, &attach); err != nil || attach.Type != "player_attached" || attach.SessionID == "" {
		t.Fatalf("bad player_attached: %s (%v)", msg, err)
	}

	// Server opens the wire for the session.
	wire, _, err := websocket.Dial(ctx, base+"/ws/server/wire/"+attach.SessionID, nil)
	if err != nil {
		t.Fatalf("wire dial: %v", err)
	}
	t.Cleanup(func() { _ = wire.Close(websocket.StatusNormalClosure, "") })

	// Frame server→player, verbatim.
	frame := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0xDE, 0xAD, 0xBE, 0xEF}
	if err := wire.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("send frame: %v", err)
	}
	typ, got, err := player.Read(ctx)
	if err != nil {
		t.Fatalf("player read frame: %v", err)
	}
	if typ != websocket.MessageBinary || string(got) != string(frame) {
		t.Fatalf("frame mismatch: type=%v got=% x want=% x", typ, got, frame)
	}

	// Input player→server, verbatim.
	input := []byte("INPUT-EVENT")
	if err := player.Write(ctx, websocket.MessageBinary, input); err != nil {
		t.Fatalf("send input: %v", err)
	}
	_, gotIn, err := wire.Read(ctx)
	if err != nil {
		t.Fatalf("wire read input: %v", err)
	}
	if string(gotIn) != string(input) {
		t.Fatalf("input mismatch: got %q want %q", gotIn, input)
	}

	// Player disconnect → server sees player_detached.
	_ = player.Close(websocket.StatusNormalClosure, "")
	_, msg2, err := sideband.Read(ctx)
	if err != nil {
		t.Fatalf("read player_detached: %v", err)
	}
	var detach struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg2, &detach); detach.Type != "player_detached" {
		t.Fatalf("expected player_detached, got %s", msg2)
	}
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
