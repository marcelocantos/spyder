// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package logcollect runs server-managed TCP listeners that receive
// log lines pushed from apps configured with LOG_TARGET=host:port.
// Each capture session binds its own kernel-assigned port, so a
// connection on a given port is unambiguously from the app that was
// launched against that LOG_TARGET — no in-band tagging required.
//
// The model parallels [logcapture] (server-side capture of the
// device's own log stream) but inverts the data flow: the device
// connects out to spyder rather than spyder polling the device.
// Sessions auto-expire on TTL, evict FIFO when the buffer is full,
// and can be queried incrementally without stopping.
//
// (🎯T73 follow-up: env passthrough lets users inject LOG_TARGET at
// launch; this package gives them somewhere for the app to dial.)
package logcollect

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	// DefaultTTL bounds an unstopped session's lifetime. After this
	// elapses with no Get or Stop call, the session is auto-torn-down
	// and its listener closed.
	DefaultTTL = 5 * time.Minute

	// MaxTTL caps the user-supplied TTL.
	MaxTTL = 24 * time.Hour

	// DefaultMaxBytes bounds a session's buffer at ~50 MB of accumulated
	// payload (line bytes + per-line overhead).
	DefaultMaxBytes = 50 * 1024 * 1024

	// DefaultMaxLines bounds a session at 100k lines.
	DefaultMaxLines = 100_000

	// MaxLineBytes caps any single log line that arrives over the wire.
	// Lines longer than this are truncated before buffering.
	MaxLineBytes = 64 * 1024

	// sweepInterval is the cadence for the TTL-expiry sweeper.
	sweepInterval = 30 * time.Second
)

// Line is a single received log entry annotated with arrival timestamp
// and the remote socket address (useful when an app reconnects mid-
// session or when distinguishing multiple concurrent peers on the same
// listener).
type Line struct {
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"` // remote addr "ip:port"
	Message   string    `json:"message"`
}

// approxLineBytes is the conservative per-line accounting used to
// enforce MaxBytes. Close enough for budget-firing decisions.
func approxLineBytes(l Line) int {
	return len(l.Message) + len(l.Source) + 24 // Timestamp + slop
}

// Session is an active capture bound to a listener on a single port.
type Session struct {
	ID        string
	Port      int
	Owner     string
	StartedAt time.Time
	TTL       time.Duration
	MaxBytes  int
	MaxLines  int

	listener net.Listener
	// cancel terminates the accept loop and per-connection readers.
	cancel context.CancelFunc
	// done fires when the accept loop has exited.
	done chan struct{}

	// mu guards everything below.
	mu            sync.Mutex
	buf           []Line
	bufBytes      int
	dropped       int
	lastUsed      time.Time
	connections   int // total connections accepted (lifetime)
	activeConns   int // currently open
	bytesReceived int // total bytes read off the wire (lifetime)
}

func (s *Session) append(l Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sz := approxLineBytes(l)
	s.buf = append(s.buf, l)
	s.bufBytes += sz
	s.bytesReceived += len(l.Message)

	for len(s.buf) > s.MaxLines || s.bufBytes > s.MaxBytes {
		evicted := s.buf[0]
		s.bufBytes -= approxLineBytes(evicted)
		s.buf = s.buf[1:]
		s.dropped++
	}
	s.lastUsed = time.Now()
}

func (s *Session) drain() (lines []Line, dropped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf == nil {
		lines = []Line{}
	} else {
		lines = s.buf
	}
	dropped = s.dropped
	s.buf = nil
	s.bufBytes = 0
	s.dropped = 0
	s.lastUsed = time.Now()
	return
}

func (s *Session) idle(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.lastUsed) >= s.TTL
}

// Manager owns the session table. One per daemon.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session

	closed  chan struct{}
	closeFn func()
}

// NewManager returns a Manager with a background TTL sweeper running.
func NewManager() *Manager {
	m := &Manager{
		sessions: map[string]*Session{},
		closed:   make(chan struct{}),
	}
	swCtx, swCancel := context.WithCancel(context.Background())
	m.closeFn = swCancel
	go m.sweep(swCtx)
	return m
}

// StartParams configures a new listener.
type StartParams struct {
	Owner    string
	TTL      time.Duration // 0 → DefaultTTL
	MaxBytes int           // 0 → DefaultMaxBytes
	MaxLines int           // 0 → DefaultMaxLines
}

// Start binds a fresh kernel-assigned TCP port on all interfaces and
// starts an accept loop that streams received lines into a bounded
// buffer. Returns the new session.
func (m *Manager) Start(p StartParams) (*Session, error) {
	ttl := p.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		return nil, fmt.Errorf("logcollect: ttl %s exceeds max %s", ttl, MaxTTL)
	}
	maxBytes := p.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	maxLines := p.MaxLines
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}

	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("logcollect: id: %w", err)
	}

	// Bind to all interfaces so the device can reach the listener from
	// the LAN. Port 0 → kernel-assigned.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("logcollect: listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:        id,
		Port:      port,
		Owner:     p.Owner,
		StartedAt: time.Now(),
		TTL:       ttl,
		MaxBytes:  maxBytes,
		MaxLines:  maxLines,
		listener:  ln,
		cancel:    cancel,
		done:      make(chan struct{}),
		lastUsed:  time.Now(),
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go s.acceptLoop(ctx)

	slog.Info("logcollect: session started",
		"session_id", id, "port", port, "owner", p.Owner,
		"ttl", ttl, "max_bytes", maxBytes, "max_lines", maxLines)

	return s, nil
}

// acceptLoop pulls connections off the listener and spawns a reader
// per connection. Exits when the listener is closed or ctx is done.
func (s *Session) acceptLoop(ctx context.Context) {
	defer close(s.done)

	// When ctx fires, close the listener so Accept unblocks.
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()

	var connWG sync.WaitGroup
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("logcollect: accept error",
					"session_id", s.ID, "error", err)
			}
			break
		}
		connWG.Add(1)
		s.mu.Lock()
		s.connections++
		s.activeConns++
		s.mu.Unlock()
		go func(c net.Conn) {
			defer connWG.Done()
			s.readConn(ctx, c)
			s.mu.Lock()
			s.activeConns--
			s.mu.Unlock()
		}(conn)
	}
	connWG.Wait()
}

// readConn consumes the connection line-by-line, appending each line
// (truncated to MaxLineBytes if oversized) to the session buffer.
// Returns when the peer closes, ctx fires, or read errors.
func (s *Session) readConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	remote := conn.RemoteAddr().String()
	slog.Debug("logcollect: connection opened",
		"session_id", s.ID, "remote", remote)

	// Close the connection if ctx fires so the Scanner unblocks.
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopWatcher:
		}
	}()

	sc := bufio.NewScanner(conn)
	// Raise the per-line cap above bufio's default (64 KB).
	buf := make([]byte, 0, 8*1024)
	sc.Buffer(buf, MaxLineBytes)
	for sc.Scan() {
		text := sc.Text()
		s.append(Line{
			Timestamp: time.Now(),
			Source:    remote,
			Message:   text,
		})
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		slog.Debug("logcollect: scan error",
			"session_id", s.ID, "remote", remote, "error", err)
	}
}

// Get returns the buffered lines without stopping the session.
func (m *Manager) Get(id string) (*GetResult, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("logcollect: no such session: %s", id)
	}
	lines, dropped := s.drain()
	return &GetResult{
		SessionID:    id,
		CapturedAt:   time.Now(),
		Lines:        lines,
		DroppedLines: dropped,
	}, nil
}

// Stop returns the buffered lines and tears the session down.
func (m *Manager) Stop(id string) (*StopResult, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("logcollect: no such session: %s", id)
	}
	s.cancel()
	<-s.done
	lines, dropped := s.drain()
	slog.Info("logcollect: session stopped",
		"session_id", id, "lines", len(lines), "dropped", dropped,
		"connections", s.connections, "bytes_received", s.bytesReceived)
	return &StopResult{
		SessionID:    id,
		StoppedAt:    time.Now(),
		Lines:        lines,
		DroppedLines: dropped,
	}, nil
}

// List returns metadata for every live session.
func (m *Manager) List() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		out = append(out, SessionInfo{
			SessionID:     s.ID,
			Port:          s.Port,
			Owner:         s.Owner,
			StartedAt:     s.StartedAt,
			ExpiresAt:     s.lastUsed.Add(s.TTL),
			BufferLines:   len(s.buf),
			BufferBytes:   s.bufBytes,
			Dropped:       s.dropped,
			Connections:   s.connections,
			ActiveConns:   s.activeConns,
			BytesReceived: s.bytesReceived,
		})
		s.mu.Unlock()
	}
	return out
}

// Close terminates every live session.
func (m *Manager) Close() {
	m.closeFn()
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_, _ = m.Stop(id)
	}
}

func (m *Manager) sweep(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.mu.Lock()
			expired := make([]string, 0)
			for id, s := range m.sessions {
				if s.idle(now) {
					expired = append(expired, id)
				}
			}
			m.mu.Unlock()
			for _, id := range expired {
				slog.Info("logcollect: session expired by TTL", "session_id", id)
				_, _ = m.Stop(id)
			}
		}
	}
}

// LANHosts returns IPv4 addresses on non-loopback, non-link-local
// interfaces that an app on a LAN device can dial to reach this host.
// On a typical laptop this is the Wi-Fi address plus any wired/
// thunderbolt links. Returned in interface enumeration order.
func LANHosts() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("net.Interfaces: %w", err)
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out, nil
}

// GetResult is the payload returned by Manager.Get.
type GetResult struct {
	SessionID    string    `json:"session_id"`
	CapturedAt   time.Time `json:"captured_at"`
	Lines        []Line    `json:"lines"`
	DroppedLines int       `json:"dropped_lines,omitempty"`
}

// StopResult is the payload returned by Manager.Stop.
type StopResult struct {
	SessionID    string    `json:"session_id"`
	StoppedAt    time.Time `json:"stopped_at"`
	Lines        []Line    `json:"lines"`
	DroppedLines int       `json:"dropped_lines,omitempty"`
}

// SessionInfo is the per-session record returned by Manager.List.
type SessionInfo struct {
	SessionID     string    `json:"session_id"`
	Port          int       `json:"port"`
	Owner         string    `json:"owner,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	BufferLines   int       `json:"buffer_lines"`
	BufferBytes   int       `json:"buffer_bytes"`
	Dropped       int       `json:"dropped_lines,omitempty"`
	Connections   int       `json:"connections"`    // lifetime total
	ActiveConns   int       `json:"active_conns"`   // currently open
	BytesReceived int       `json:"bytes_received"` // lifetime total
}

var errClosed = errors.New("logcollect: closed")

// newID returns a URL-safe random session id, 16 hex digits.
func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var _ = errClosed // reserved for future cancel propagation
