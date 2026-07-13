// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package streamrelay is spyder's dev H.264 stream relay (🎯T91.4 / T92.2) —
// the payload-agnostic middle-man between a ge game SERVER
// and a PLAYER. It speaks ge's existing brokered wire so a ge server only has
// to repoint its connect URL to spyder:
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
//
// 🎯T96: attach/detach logs and GET /stream/sessions expose peer remotes,
// path class (loopback|lan|public|unknown), and rolling byte/frame counters
// so lag can be attributed without packet capture.
package streamrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	remote   string // sideband peer (http.Request.RemoteAddr)
	writeMu  sync.Mutex
}

func (s *serverConn) send(ctx context.Context, v any) error {
	b, _ := json.Marshal(v)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.sideband.Write(ctx, websocket.MessageText, b)
}

// hopRates tracks lifetime totals plus a ~1s rolling window of message
// counts/sizes for one pipe direction. Opaque pipe telemetry: every WebSocket
// message is one "frame" (video AU or input blob) — no codec decode required.
type hopRates struct {
	frames atomic.Uint64
	bytes  atomic.Uint64
	maxSz  atomic.Uint64
	lastSz atomic.Uint64

	mu        sync.Mutex
	winStart  time.Time
	winFrames uint64
	winBytes  uint64
	// Last completed ≥1s window (0 until the first window closes).
	fps1s         float64
	bytesPerSec1s float64
}

func (h *hopRates) note(n int) {
	if n < 0 {
		n = 0
	}
	sz := uint64(n)
	h.frames.Add(1)
	h.bytes.Add(sz)
	h.lastSz.Store(sz)
	for {
		old := h.maxSz.Load()
		if sz <= old || h.maxSz.CompareAndSwap(old, sz) {
			break
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if h.winStart.IsZero() {
		h.winStart = now
	}
	h.winFrames++
	h.winBytes += sz
	if d := now.Sub(h.winStart); d >= time.Second {
		sec := d.Seconds()
		if sec > 0 {
			h.fps1s = float64(h.winFrames) / sec
			h.bytesPerSec1s = float64(h.winBytes) / sec
		}
		h.winFrames = 0
		h.winBytes = 0
		h.winStart = now
	}
}

func (h *hopRates) snapshot(age time.Duration) (frames, bytes, maxSz, lastSz uint64, fps1s, bps1s, fpsAvg, bpsAvg float64) {
	frames = h.frames.Load()
	bytes = h.bytes.Load()
	maxSz = h.maxSz.Load()
	lastSz = h.lastSz.Load()
	h.mu.Lock()
	fps1s = h.fps1s
	bps1s = h.bytesPerSec1s
	// If the current window is already ≥250ms and we have samples, expose a
	// partial window so early GETs are not stuck at 0 until the first full second.
	if fps1s == 0 && h.winFrames > 0 && !h.winStart.IsZero() {
		if d := time.Since(h.winStart); d >= 250*time.Millisecond {
			sec := d.Seconds()
			if sec > 0 {
				fps1s = float64(h.winFrames) / sec
				bps1s = float64(h.winBytes) / sec
			}
		}
	}
	h.mu.Unlock()
	if sec := age.Seconds(); sec > 0 {
		fpsAvg = float64(frames) / sec
		bpsAvg = float64(bytes) / sec
	}
	return
}

type session struct {
	id           string
	server       *serverConn
	player       *websocket.Conn
	playerRemote string
	wireRemote   string // set when the server opens the per-session wire
	wireCh       chan *websocket.Conn
	started      time.Time

	s2p hopRates // server → player (video messages)
	p2s hopRates // player → server (input messages)
}

func (s *session) noteS2P(n int) { s.s2p.note(n) }
func (s *session) noteP2S(n int) { s.p2s.note(n) }

// ServerInfo is a connected streaming server (for the dashboard catalogue).
type ServerInfo struct {
	Name      string    `json:"name"`
	Sessions  int       `json:"sessions"`
	Remote    string    `json:"remote,omitempty"`
	PathClass PathClass `json:"path_class,omitempty"`
}

// SessionInfo is a live player↔server pairing with hop telemetry (🎯T96).
// Rates need no codec: each WebSocket binary message is one counted "frame".
type SessionInfo struct {
	ID              string    `json:"session_id"`
	ServerName      string    `json:"server_name"`
	PlayerRemote    string    `json:"player_remote"`
	PlayerPathClass PathClass `json:"player_path_class"`
	ServerRemote    string    `json:"server_remote"`
	ServerPathClass PathClass `json:"server_path_class"`
	WireRemote      string    `json:"wire_remote,omitempty"`
	WirePathClass   PathClass `json:"wire_path_class,omitempty"`
	AgeMs           int64     `json:"age_ms"`

	// Lifetime totals (server → player / player → server).
	FramesS2P uint64 `json:"frames_s2p"`
	BytesS2P  uint64 `json:"bytes_s2p"`
	FramesP2S uint64 `json:"frames_p2s"`
	BytesP2S  uint64 `json:"bytes_p2s"`

	// Size extremes for the video direction (opaque message length).
	MaxFrameBytesS2P  uint64 `json:"max_frame_bytes_s2p"`
	LastFrameBytesS2P uint64 `json:"last_frame_bytes_s2p"`
	AvgFrameBytesS2P  uint64 `json:"avg_frame_bytes_s2p,omitempty"`

	// Lifetime average rates since session start.
	FPSAvgS2P         float64 `json:"fps_avg_s2p"`
	BytesPerSecAvgS2P float64 `json:"bytes_per_sec_avg_s2p"`
	FPSAvgP2S         float64 `json:"fps_avg_p2s"`
	BytesPerSecAvgP2S float64 `json:"bytes_per_sec_avg_p2s"`

	// ~1s rolling window rates (best for spotting hitch windows).
	FPS1sS2P         float64 `json:"fps_1s_s2p"`
	BytesPerSec1sS2P float64 `json:"bytes_per_sec_1s_s2p"`
	FPS1sP2S         float64 `json:"fps_1s_p2s"`
	BytesPerSec1sP2S float64 `json:"bytes_per_sec_1s_p2s"`
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
	for name, sc := range r.servers {
		out = append(out, ServerInfo{
			Name:      name,
			Sessions:  counts[name],
			Remote:    sc.remote,
			PathClass: ClassifyRemote(sc.remote),
		})
	}
	return out
}

// Sessions returns live pairings with hop telemetry.
func (r *Relay) Sessions() []SessionInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	out := make([]SessionInfo, 0, len(r.sessions))
	for _, s := range r.sessions {
		age := now.Sub(s.started)
		fS2P, bS2P, maxS2P, lastS2P, fps1sS2P, bps1sS2P, fpsAvgS2P, bpsAvgS2P := s.s2p.snapshot(age)
		fP2S, bP2S, _, _, fps1sP2S, bps1sP2S, fpsAvgP2S, bpsAvgP2S := s.p2s.snapshot(age)
		info := SessionInfo{
			ID:                s.id,
			ServerName:        s.server.name,
			PlayerRemote:      s.playerRemote,
			PlayerPathClass:   ClassifyRemote(s.playerRemote),
			ServerRemote:      s.server.remote,
			ServerPathClass:   ClassifyRemote(s.server.remote),
			WireRemote:        s.wireRemote,
			AgeMs:             age.Milliseconds(),
			FramesS2P:         fS2P,
			BytesS2P:          bS2P,
			FramesP2S:         fP2S,
			BytesP2S:          bP2S,
			MaxFrameBytesS2P:  maxS2P,
			LastFrameBytesS2P: lastS2P,
			FPSAvgS2P:         fpsAvgS2P,
			BytesPerSecAvgS2P: bpsAvgS2P,
			FPSAvgP2S:         fpsAvgP2S,
			BytesPerSecAvgP2S: bpsAvgP2S,
			FPS1sS2P:          fps1sS2P,
			BytesPerSec1sS2P:  bps1sS2P,
			FPS1sP2S:          fps1sP2S,
			BytesPerSec1sP2S:  bps1sP2S,
		}
		if fS2P > 0 {
			info.AvgFrameBytesS2P = bS2P / fS2P
		}
		if s.wireRemote != "" {
			info.WirePathClass = ClassifyRemote(s.wireRemote)
		}
		out = append(out, info)
	}
	return out
}

// HandleServerList handles GET /stream/servers: a JSON list of connected
// streaming servers, for the dashboard's stream panel.
func (r *Relay) HandleServerList(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"servers": r.Servers()})
}

// HandleSessionList handles GET /stream/sessions: live hop telemetry (🎯T96).
func (r *Relay) HandleSessionList(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sessions": r.Sessions()})
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
	remote := req.RemoteAddr
	sc := &serverConn{name: name, sideband: c, remote: remote}
	r.mu.Lock()
	r.servers[name] = sc
	r.mu.Unlock()
	slog.Info("streamrelay: server connected",
		"name", name,
		"remote", remote,
		"path_class", ClassifyRemote(remote),
	)

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
	slog.Info("streamrelay: server disconnected",
		"name", name,
		"remote", remote,
		"path_class", ClassifyRemote(remote),
	)
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
	r.mu.Lock()
	if s := r.sessions[id]; s != nil {
		s.wireRemote = req.RemoteAddr
	}
	r.mu.Unlock()
	slog.Info("streamrelay: wire open",
		"session", id,
		"remote", req.RemoteAddr,
		"path_class", ClassifyRemote(req.RemoteAddr),
	)
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
// to reach the old broker), so spyder serves it here and repoints are unnecessary.
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

	playerRemote := req.RemoteAddr
	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("s%d", r.seq)
	sess := &session{
		id:           id,
		server:       sc,
		player:       c,
		playerRemote: playerRemote,
		wireCh:       make(chan *websocket.Conn, 1),
		started:      time.Now(),
	}
	r.sessions[id] = sess
	r.mu.Unlock()
	slog.Info("streamrelay: player attached",
		"server", name,
		"session", id,
		"player_remote", playerRemote,
		"player_path_class", ClassifyRemote(playerRemote),
		"server_remote", sc.remote,
		"server_path_class", ClassifyRemote(sc.remote),
	)

	ctx := req.Context()
	defer func() {
		var (
			framesS2P, bytesS2P, maxS2P uint64
			framesP2S, bytesP2S         uint64
			ageMs                       int64
			fpsAvg, bpsAvg              float64
		)
		r.mu.Lock()
		if s := r.sessions[id]; s != nil {
			age := time.Since(s.started)
			ageMs = age.Milliseconds()
			var fps1s, bps1s float64
			framesS2P, bytesS2P, maxS2P, _, fps1s, bps1s, fpsAvg, bpsAvg = s.s2p.snapshot(age)
			framesP2S, bytesP2S, _, _, _, _, _, _ = s.p2s.snapshot(age)
			_ = fps1s
			_ = bps1s
		}
		delete(r.sessions, id)
		r.mu.Unlock()
		_ = sc.send(context.Background(), map[string]any{"type": "player_detached", "session_id": id})
		_ = c.Close(websocket.StatusNormalClosure, "")
		slog.Info("streamrelay: player detached",
			"server", name,
			"session", id,
			"player_remote", playerRemote,
			"player_path_class", ClassifyRemote(playerRemote),
			"frames_s2p", framesS2P,
			"bytes_s2p", bytesS2P,
			"frames_p2s", framesP2S,
			"bytes_p2s", bytesP2S,
			"max_frame_bytes_s2p", maxS2P,
			"fps_avg_s2p", fpsAvg,
			"bytes_per_sec_avg_s2p", bpsAvg,
			"age_ms", ageMs,
		)
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
	go func() {
		pipe(pctx, wire, c, sess.noteS2P)
		pcancel()
	}()
	go func() {
		pipe(pctx, c, wire, sess.noteP2S)
		pcancel()
	}()
	<-pctx.Done()
}

// pipe copies whole WebSocket messages from src to dst verbatim (same message
// type) until either side errors or ctx is cancelled. onMsg is invoked with
// each payload size after a successful write (for 🎯T96 counters).
func pipe(ctx context.Context, src, dst *websocket.Conn, onMsg func(int)) {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
		if onMsg != nil {
			onMsg(len(data))
		}
	}
}
