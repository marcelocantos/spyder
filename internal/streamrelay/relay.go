// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package streamrelay is spyder's dev H.264 stream relay (🎯T91.4 / T92.2) —
// the payload-agnostic middle-man that replaces ged between a ge game SERVER
// and a PLAYER. It speaks ge's existing brokered wire so a ge server only has
// to repoint its connect URL from ged to spyder:
//
//   - server control  : GET /ws/server?name=<name>   (WebSocket, JSON text)
//   - per-session wire : GET /ws/server/wire/<id>     (WebSocket, binary)
//   - browser player   : GET /stream/player/<name>    (WebSocket, binary)
//
// When a player connects, the relay allocates a session id, tells the server
// {"type":"player_attached","session_id":id} on its control socket; the server
// dials back the matching wire, and the relay pipes frames wire→player and
// input player→wire verbatim (it never decodes — ge owns the codec). On player
// disconnect it sends {"type":"player_detached"}. LAN/trusted dev only.
package streamrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// maxFrameBytes bounds a single wire message (H.264 access unit or input).
const maxFrameBytes = 16 << 20

// Relay orchestrates server/player pairing and byte-piping.
type Relay struct {
	mu       sync.Mutex
	servers  map[string]*serverConn // by advertised name
	sessions map[string]*session    // by session id
	seq      uint64
}

// New returns an empty relay.
func New() *Relay {
	return &Relay{servers: map[string]*serverConn{}, sessions: map[string]*session{}}
}

type serverConn struct {
	name     string
	sideband *websocket.Conn
	writeMu  sync.Mutex // serialises control writes
}

func (s *serverConn) send(ctx context.Context, v any) error {
	b, _ := json.Marshal(v)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.sideband.Write(ctx, websocket.MessageText, b)
}

type session struct {
	id     string
	server *serverConn
	player *websocket.Conn
	wireCh chan *websocket.Conn // the server's wire, once it dials in
}

// ServerInfo is a connected streaming server (for the dashboard catalogue).
type ServerInfo struct {
	Name     string `json:"name"`
	Sessions int    `json:"sessions"`
}

// Servers lists connected streaming servers.
func (r *Relay) Servers() []ServerInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[string]int{}
	for _, s := range r.sessions {
		counts[s.server.name]++
	}
	out := make([]ServerInfo, 0, len(r.servers))
	for name := range r.servers {
		out = append(out, ServerInfo{Name: name, Sessions: counts[name]})
	}
	return out
}

// HandleServerList handles GET /stream/servers: a JSON list of connected
// streaming servers, for the dashboard's stream panel.
func (r *Relay) HandleServerList(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"servers": r.Servers()})
}

// HandleServerSideband handles GET /ws/server?name=<name>: the server's control
// channel. The relay tracks the server and keeps the socket open (draining any
// messages) until it closes.
func (r *Relay) HandleServerSideband(w http.ResponseWriter, req *http.Request) {
	name := req.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name query param required", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	sc := &serverConn{name: name, sideband: c}
	r.mu.Lock()
	r.servers[name] = sc
	r.mu.Unlock()
	slog.Info("streamrelay: server connected", "name", name)

	ctx := req.Context()
	// Drain the control socket until it closes (the server sends a hello and
	// otherwise mostly listens). We only need to detect disconnect.
	for {
		if _, _, err := c.Read(ctx); err != nil {
			break
		}
	}
	r.mu.Lock()
	if r.servers[name] == sc {
		delete(r.servers, name)
	}
	r.mu.Unlock()
	slog.Info("streamrelay: server disconnected", "name", name)
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// HandleServerWire handles GET /ws/server/wire/<id>: the server dialling in the
// per-session video wire after a player_attached. It's handed to the waiting
// player session.
func (r *Relay) HandleServerWire(w http.ResponseWriter, req *http.Request) {
	id := strings.TrimPrefix(req.URL.Path, "/ws/server/wire/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	sess := r.sessions[id]
	r.mu.Unlock()
	if sess == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	c, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(maxFrameBytes)
	// Hand the wire to the player goroutine and block until the session ends
	// (closing here would tear the wire down).
	select {
	case sess.wireCh <- c:
	case <-req.Context().Done():
		_ = c.Close(websocket.StatusNormalClosure, "")
		return
	}
	<-req.Context().Done()
}

// HandlePlayerConnect handles GET /stream/player/<name>: a browser attaching to
// server <name> (the name is the last path segment).
func (r *Relay) HandlePlayerConnect(w http.ResponseWriter, req *http.Request) {
	name := strings.TrimPrefix(req.URL.Path, "/stream/player/")
	if name == "" || strings.Contains(name, "/") {
		http.Error(w, "server name required", http.StatusBadRequest)
		return
	}
	r.servePlayer(w, req, name)
}

// HandlePlayerWire handles GET /ws/wire?preference=<name>&name=<name>: ge's
// NATIVE player (PlayerWireBridge) attaching to server <name>. Same pairing as
// the browser path — the native player just dials a different URL (it was built
// to reach ged), so spyder serves it here and repoints are unnecessary.
func (r *Relay) HandlePlayerWire(w http.ResponseWriter, req *http.Request) {
	name := req.URL.Query().Get("preference")
	if name == "" {
		name = req.URL.Query().Get("name")
	}
	if name == "" {
		http.Error(w, "preference or name query param required", http.StatusBadRequest)
		return
	}
	r.servePlayer(w, req, name)
}

// servePlayer pairs a connecting player with server <name>: allocate a session,
// ask the server to open a wire, then pipe frames wire→player and input
// player→wire until either side closes. Shared by the browser and native paths.
func (r *Relay) servePlayer(w http.ResponseWriter, req *http.Request, name string) {
	r.mu.Lock()
	sc := r.servers[name]
	r.mu.Unlock()
	if sc == nil {
		http.Error(w, "no such streaming server", http.StatusNotFound)
		return
	}

	c, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(maxFrameBytes)

	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("s%d", r.seq)
	sess := &session{id: id, server: sc, player: c, wireCh: make(chan *websocket.Conn, 1)}
	r.sessions[id] = sess
	r.mu.Unlock()
	slog.Info("streamrelay: player attached", "server", name, "session", id)

	ctx := req.Context()
	defer func() {
		r.mu.Lock()
		delete(r.sessions, id)
		r.mu.Unlock()
		_ = sc.send(context.Background(), map[string]any{"type": "player_detached", "session_id": id})
		_ = c.Close(websocket.StatusNormalClosure, "")
		slog.Info("streamrelay: player detached", "server", name, "session", id)
	}()

	// Ask the server to open the wire for this session.
	if err := sc.send(ctx, map[string]any{"type": "player_attached", "session_id": id}); err != nil {
		return
	}

	// Wait for the server's wire.
	var wire *websocket.Conn
	select {
	case wire = <-sess.wireCh:
	case <-ctx.Done():
		return
	}
	defer wire.Close(websocket.StatusNormalClosure, "")

	// Pipe both directions verbatim. Cancelling pctx when either pipe ends
	// unblocks the other's Read/Write immediately (no close-handshake wait).
	pctx, pcancel := context.WithCancel(ctx)
	defer pcancel()
	go func() { pipe(pctx, wire, c); pcancel() }() // frames server→player
	go func() { pipe(pctx, c, wire); pcancel() }() // input player→server
	<-pctx.Done()
}

// pipe copies whole WebSocket messages from src to dst verbatim (same message
// type) until either side errors or ctx is cancelled.
func pipe(ctx context.Context, src, dst *websocket.Conn) {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
	}
}
