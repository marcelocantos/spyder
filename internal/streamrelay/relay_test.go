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

	// 🎯T96: session catalogue shows counters after the frame exchange.
	sessions := relay.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions: got %d want 1", len(sessions))
	}
	si := sessions[0]
	if si.ID != attach.SessionID {
		t.Fatalf("session_id: got %q want %q", si.ID, attach.SessionID)
	}
	if si.ServerName != "game1" {
		t.Fatalf("server_name: got %q", si.ServerName)
	}
	if si.FramesS2P < 1 || si.BytesS2P < uint64(len(frame)) {
		t.Fatalf("s2p counters: frames=%d bytes=%d", si.FramesS2P, si.BytesS2P)
	}
	if si.FramesP2S < 1 || si.BytesP2S < uint64(len(input)) {
		t.Fatalf("p2s counters: frames=%d bytes=%d", si.FramesP2S, si.BytesP2S)
	}
	if si.PlayerPathClass == "" || si.ServerPathClass == "" {
		t.Fatalf("path classes empty: player=%q server=%q", si.PlayerPathClass, si.ServerPathClass)
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

// TestRelay_SessionListHTTP exercises GET /stream/sessions JSON shape (🎯T96).
func TestRelay_SessionListHTTP(t *testing.T) {
	relay := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/server", relay.HandleServerSideband)
	mux.HandleFunc("/ws/server/wire/", relay.HandleServerWire)
	mux.HandleFunc("/stream/player/", relay.HandlePlayerConnect)
	mux.HandleFunc("/stream/sessions", relay.HandleSessionList)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	baseWS := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sideband, _, err := websocket.Dial(ctx, baseWS+"/ws/server?name=obs", nil)
	if err != nil {
		t.Fatalf("server dial: %v", err)
	}
	t.Cleanup(func() { _ = sideband.Close(websocket.StatusNormalClosure, "") })
	if !waitFor(2*time.Second, func() bool { return len(relay.Servers()) == 1 }) {
		t.Fatal("server not registered")
	}

	player, _, err := websocket.Dial(ctx, baseWS+"/stream/player/obs", nil)
	if err != nil {
		t.Fatalf("player dial: %v", err)
	}
	t.Cleanup(func() { _ = player.Close(websocket.StatusNormalClosure, "") })

	_, msg, err := sideband.Read(ctx)
	if err != nil {
		t.Fatalf("read player_attached: %v", err)
	}
	var attach struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(msg, &attach); err != nil || attach.SessionID == "" {
		t.Fatalf("bad player_attached: %s", msg)
	}
	wire, _, err := websocket.Dial(ctx, baseWS+"/ws/server/wire/"+attach.SessionID, nil)
	if err != nil {
		t.Fatalf("wire dial: %v", err)
	}
	t.Cleanup(func() { _ = wire.Close(websocket.StatusNormalClosure, "") })
	_ = wire.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3, 4})
	if _, _, err := player.Read(ctx); err != nil {
		t.Fatalf("player read: %v", err)
	}

	// Give the pipe goroutine a moment to record counters.
	if !waitFor(2*time.Second, func() bool {
		for _, s := range relay.Sessions() {
			if s.FramesS2P >= 1 {
				return true
			}
		}
		return false
	}) {
		t.Fatal("counters not updated")
	}

	resp, err := http.Get(srv.URL + "/stream/sessions")
	if err != nil {
		t.Fatalf("GET /stream/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sessions) != 1 {
		t.Fatalf("sessions: got %d", len(body.Sessions))
	}
	if body.Sessions[0].ID != attach.SessionID {
		t.Fatalf("id %q want %q", body.Sessions[0].ID, attach.SessionID)
	}
	if body.Sessions[0].FramesS2P < 1 {
		t.Fatalf("frames_s2p=%d", body.Sessions[0].FramesS2P)
	}
	if body.Sessions[0].PlayerPathClass == PathUnknown && body.Sessions[0].PlayerRemote != "" {
		// httptest RemoteAddr is usually 127.0.0.1 — expect loopback.
	}
	if body.Sessions[0].PlayerPathClass != PathLoopback && body.Sessions[0].PlayerPathClass != PathLAN {
		t.Fatalf("unexpected player_path_class %q remote %q",
			body.Sessions[0].PlayerPathClass, body.Sessions[0].PlayerRemote)
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
