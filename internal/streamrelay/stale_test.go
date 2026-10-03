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

func staleFixture(t *testing.T, relay *Relay) (string, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/server", relay.HandleServerSideband)
	mux.HandleFunc("/ws/server/wire/", relay.HandleServerWire)
	mux.HandleFunc("/stream/player/", relay.HandlePlayerConnect)
	mux.HandleFunc("/stream/sessions", relay.HandleSessionList)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func staleDial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func staleAttach(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("attach notification: %v", err)
	}
	var msg struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.Type != "player_attached" || msg.SessionID == "" {
		t.Fatalf("attach notification: %s (%v)", data, err)
	}
	return msg.SessionID
}

func staleList(t *testing.T, url string) []SessionInfo {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(url + "/stream/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Sessions
}

// Abrupt sideband death used to strand a player waiting forever for the wire.
// A healthy second game must remain present and pipe normally.
func TestRelay_DeadSidebandCullsOnlyItsSessions(t *testing.T) {
	relay := New()
	httpURL, wsURL := staleFixture(t, relay)
	dead := staleDial(t, wsURL+"/ws/server?name=dead")
	healthy := staleDial(t, wsURL+"/ws/server?name=healthy")
	if !waitFor(time.Second, func() bool { return len(relay.Servers()) == 2 }) {
		t.Fatal("servers not registered")
	}
	badPlayer := staleDial(t, wsURL+"/stream/player/dead")
	badID := staleAttach(t, dead)
	goodPlayer := staleDial(t, wsURL+"/stream/player/healthy")
	goodID := staleAttach(t, healthy)
	goodWire := staleDial(t, wsURL+"/ws/server/wire/"+goodID)
	if !waitFor(time.Second, func() bool { return len(staleList(t, httpURL)) == 2 }) {
		t.Fatal("sessions absent")
	}

	_ = dead.CloseNow() // kill -9-like: no graceful close handshake
	if !waitFor(2*time.Second, func() bool {
		ss := staleList(t, httpURL)
		return len(ss) == 1 && ss[0].ID == goodID
	}) {
		t.Fatalf("dead session %s retained: %+v", badID, staleList(t, httpURL))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := badPlayer.Read(ctx); err == nil {
		t.Fatal("dead player's socket stayed open")
	}
	if err := goodWire.Write(ctx, websocket.MessageBinary, []byte("healthy")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := goodPlayer.Read(ctx); err != nil || string(got) != "healthy" {
		t.Fatalf("healthy pipe: %q %v", got, err)
	}
	_ = goodWire.CloseNow() // established wire dies without a close handshake
	if !waitFor(2*time.Second, func() bool { return len(staleList(t, httpURL)) == 0 }) {
		t.Fatalf("dead wire retained a session: %+v", staleList(t, httpURL))
	}
}

// Replacements must not leave waiters or let a late wire for an evicted ID
// consume the current server's attach. A server that never opens its wire
// must also fail promptly rather than hang at SessionConfig indefinitely.
func TestRelay_ReplaceThenAttachAndBoundMissingWire(t *testing.T) {
	relay := New()
	httpURL, wsURL := staleFixture(t, relay)
	old := staleDial(t, wsURL+"/ws/server?name=game")
	if !waitFor(time.Second, func() bool { return len(relay.Servers()) == 1 }) {
		t.Fatal("old server absent")
	}
	oldPlayer := staleDial(t, wsURL+"/stream/player/game")
	oldID := staleAttach(t, old)
	for i := 0; i < 10; i++ {
		next := staleDial(t, wsURL+"/ws/server?name=game")
		if !waitFor(time.Second, func() bool { return len(staleList(t, httpURL)) == 0 }) {
			t.Fatalf("stale sessions after replace %d", i)
		}
		old = next
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := oldPlayer.Read(ctx); err == nil {
		t.Fatal("replaced player still connected")
	}
	resp, err := http.Get(httpURL + "/ws/server/wire/" + oldID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("late old wire status %d", resp.StatusCode)
	}

	player := staleDial(t, wsURL+"/stream/player/game")
	newID := staleAttach(t, old)
	wire := staleDial(t, wsURL+"/ws/server/wire/"+newID)
	if err := wire.Write(ctx, websocket.MessageBinary, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := player.Read(ctx); err != nil || string(got) != "new" {
		t.Fatalf("fresh attach: %q %v", got, err)
	}

	unwired := staleDial(t, wsURL+"/stream/player/game")
	missingID := staleAttach(t, old)
	if !waitFor(4*time.Second, func() bool {
		ss := staleList(t, httpURL)
		for _, s := range ss {
			if s.ID == missingID {
				return false
			}
		}
		return true
	}) {
		t.Fatalf("unwired attach %s never expired", missingID)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, _, err := unwired.Read(ctx2); err == nil {
		t.Fatal("unwired player stayed connected")
	}
	ss := staleList(t, httpURL)
	if len(ss) != 1 || ss[0].ID != newID {
		t.Fatalf("healthy session lost: %+v", ss)
	}
}
