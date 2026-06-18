// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	// DefaultRequestTimeout bounds a single spyder→app call.
	DefaultRequestTimeout = 30 * time.Second

	// HelloTimeout bounds how long spyder waits for the app's first
	// frame to arrive.
	HelloTimeout = 5 * time.Second

	// DefaultLogBufferLines bounds the per-session log push buffer.
	DefaultLogBufferLines = 100_000

	// DefaultPerfBufferSamples bounds the per-session perf push buffer.
	DefaultPerfBufferSamples = 10_000
)

// KeyedListenerIdleTTL is how long a per-(device, bundle_id) listener
// survives with no live session and no activity before the sweeper
// reaps it. Declared as a var so tests can shorten it.
var KeyedListenerIdleTTL = 24 * time.Hour

// KeyedListenerSweepInterval is how often the manager sweeps keyed
// listeners looking for idle-reap candidates. Declared as a var so
// tests can shorten it.
var KeyedListenerSweepInterval = 5 * time.Minute

// AppKey identifies a (device, bundle_id) pair that the manager
// keeps a singleton listener for. Two launches of the same app on the
// same device share a listener (and port); one app on two devices
// gets two listeners.
type AppKey struct {
	DeviceID string
	BundleID string
}

// LogPush is one structured log entry pushed by the app.
type LogPush struct {
	Timestamp int64  `msgpack:"ts" json:"timestamp"`
	Level     string `msgpack:"level" json:"level"`
	Subsystem string `msgpack:"subsystem,omitempty" json:"subsystem,omitempty"`
	Format    string `msgpack:"format" json:"format"`
	Args      []any  `msgpack:"args,omitempty" json:"args,omitempty"`
	Source    string `msgpack:"-" json:"source"`
}

// PerfPush is one perf-sample batch pushed by the app.
type PerfPush struct {
	Timestamp int64              `msgpack:"ts" json:"timestamp"`
	Samples   map[string]float64 `msgpack:"samples" json:"samples"`
	Source    string             `msgpack:"-" json:"source"`
}

// pending is an in-flight request awaiting a response from the app.
type pending struct {
	id   uint64
	done chan *Envelope
}

// Session is one accepted connection from an app. Owns the read loop,
// the in-flight request table, and the per-session push buffers.
type Session struct {
	ID        string
	Port      int
	Owner     string
	StartedAt time.Time

	listener *Listener // the listener that accepted this session; may be nil in tests

	conn   net.Conn
	cancel context.CancelFunc
	done   chan struct{}

	writeMu sync.Mutex // serialises Envelope writes onto conn

	nextID uint64 // atomic

	mu       sync.Mutex
	hello    *Hello
	ack      *HelloAck
	pending  map[uint64]*pending
	closed   bool
	closeErr error

	// Push buffers (drain on read).
	logBuf      []LogPush
	logDropped  int
	perfBuf     []PerfPush
	perfDropped int

	// State captures: per-session table of background pollers that
	// sample state_query{slice} on a fixed interval. Each captureID
	// is unique within a session.
	stateCaptures map[string]*StateCapture
}

// Listener returns the listener that accepted this session. Nil for
// sessions created outside the standard accept path (legacy tests).
func (s *Session) Listener() *Listener { return s.listener }

// HelloInfo returns the app's advertised identity + supported methods.
// Returns nil before the handshake completes.
func (s *Session) HelloInfo() *Hello {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hello == nil {
		return nil
	}
	h := *s.hello
	return &h
}

// Supports reports whether the app advertised method m in its Hello.
func (s *Session) Supports(method string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hello == nil {
		return false
	}
	for _, x := range s.hello.Methods {
		if x == method {
			return true
		}
	}
	return false
}

// Call sends a request to the app and waits for the response. Returns
// the result bytes (decode with UnpackParams) or an *RPCError.
func (s *Session) Call(ctx context.Context, method string, params any, timeout time.Duration) (msgpack.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		return nil, fmt.Errorf("appchannel: session closed: %w", err)
	}
	if s.hello != nil && !s.supportsLocked(method) {
		s.mu.Unlock()
		return nil, &RPCError{Code: ErrCodeUnsupported, Message: fmt.Sprintf("app does not support %q", method)}
	}
	s.mu.Unlock()

	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	id := atomic.AddUint64(&s.nextID, 1)
	if id == 0 {
		id = atomic.AddUint64(&s.nextID, 1)
	}
	raw, err := PackParams(params)
	if err != nil {
		return nil, err
	}
	p := &pending{id: id, done: make(chan *Envelope, 1)}

	s.mu.Lock()
	s.pending[id] = p
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	if err := s.writeEnvelope(&Envelope{ID: id, Method: method, Params: raw}); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case env := <-p.done:
		if env.Error != nil {
			return nil, env.Error
		}
		return env.Result, nil
	case <-ctx.Done():
		return nil, &RPCError{Code: ErrCodeTimeout, Message: fmt.Sprintf("timeout calling %s after %s", method, timeout)}
	case <-s.done:
		return nil, &RPCError{Code: ErrCodeNotConnected, Message: "session closed"}
	}
}

// Notify sends a push (no id, no response expected).
func (s *Session) Notify(method string, params any) error {
	raw, err := PackParams(params)
	if err != nil {
		return err
	}
	return s.writeEnvelope(&Envelope{Method: method, Params: raw})
}

// DrainLogs returns the buffered log pushes and resets the buffer.
func (s *Session) DrainLogs() ([]LogPush, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.logBuf
	if out == nil {
		out = []LogPush{}
	}
	dropped := s.logDropped
	s.logBuf = nil
	s.logDropped = 0
	return out, dropped
}

// DrainPerf returns the buffered perf pushes and resets the buffer.
func (s *Session) DrainPerf() ([]PerfPush, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.perfBuf
	if out == nil {
		out = []PerfPush{}
	}
	dropped := s.perfDropped
	s.perfBuf = nil
	s.perfDropped = 0
	return out, dropped
}

// Close terminates the session; any in-flight calls receive an error.
// Background state captures are torn down so their goroutines don't
// leak past session end.
//
// Closing s.conn directly is what actually unblocks the readLoop —
// ctx cancellation cannot interrupt a blocking socket read. Without
// this, a peer that vanished without a clean TCP close (the iOS
// app-channel test case that wedged spyder for 4.8h) leaves Close()
// parked on `<-s.done` forever, because readLoop's deferred
// `conn.Close()` (the only path that would unblock the read) cannot
// run until readLoop returns.
func (s *Session) Close() error {
	s.closeStateCaptures()
	s.cancel()
	_ = s.conn.Close()
	<-s.done
	return nil
}

// supportsLocked: caller holds s.mu.
func (s *Session) supportsLocked(method string) bool {
	if s.hello == nil {
		return true // pre-handshake, allow (hello itself)
	}
	for _, x := range s.hello.Methods {
		if x == method {
			return true
		}
	}
	return false
}

func (s *Session) writeEnvelope(env *Envelope) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return WriteFrame(s.conn, env)
}

// readLoop pumps frames from conn, routing requests/responses/pushes
// to the appropriate handler. Closes the session on read error.
func (s *Session) readLoop(ctx context.Context) {
	defer close(s.done)
	defer s.conn.Close()

	for {
		env, err := ReadFrame(s.conn)
		if err != nil {
			s.mu.Lock()
			s.closed = true
			s.closeErr = err
			// Drain pending: wake every in-flight Caller with a sentinel.
			for _, p := range s.pending {
				select {
				case p.done <- &Envelope{Error: &RPCError{Code: ErrCodeNotConnected, Message: "session closed: " + err.Error()}}:
				default:
				}
			}
			s.mu.Unlock()
			if !errors.Is(err, net.ErrClosed) {
				slog.Info("appchannel: read loop ended",
					"session_id", s.ID, "remote", s.conn.RemoteAddr().String(), "error", err)
			}
			return
		}
		s.handleFrame(env)
	}
}

func (s *Session) handleFrame(env *Envelope) {
	switch {
	case env.IsResponse():
		s.mu.Lock()
		p := s.pending[env.ID]
		s.mu.Unlock()
		if p == nil {
			slog.Warn("appchannel: response for unknown id",
				"session_id", s.ID, "id", env.ID)
			return
		}
		p.done <- env
	case env.IsPush():
		s.handlePush(env)
	case env.IsRequest():
		// App-initiated requests aren't currently used; respond with
		// "method not found" rather than dropping silently.
		_ = s.writeEnvelope(&Envelope{
			ID:    env.ID,
			Error: &RPCError{Code: ErrCodeMethodNotFound, Message: fmt.Sprintf("spyder does not service %q", env.Method)},
		})
	}
}

func (s *Session) handlePush(env *Envelope) {
	switch env.Method {
	case PushLog:
		var lp LogPush
		if err := UnpackParams(env.Params, &lp); err != nil {
			slog.Debug("appchannel: bad log push", "error", err)
			return
		}
		lp.Source = s.conn.RemoteAddr().String()
		s.mu.Lock()
		if len(s.logBuf) >= DefaultLogBufferLines {
			s.logBuf = s.logBuf[1:]
			s.logDropped++
		}
		s.logBuf = append(s.logBuf, lp)
		s.mu.Unlock()
	case PushPerfCounters:
		var pp PerfPush
		if err := UnpackParams(env.Params, &pp); err != nil {
			slog.Debug("appchannel: bad perf push", "error", err)
			return
		}
		pp.Source = s.conn.RemoteAddr().String()
		s.mu.Lock()
		if len(s.perfBuf) >= DefaultPerfBufferSamples {
			s.perfBuf = s.perfBuf[1:]
			s.perfDropped++
		}
		s.perfBuf = append(s.perfBuf, pp)
		s.mu.Unlock()
	default:
		slog.Debug("appchannel: unknown push", "method", env.Method)
	}
}

// handshake reads the first frame from the app, expects it to be a
// `hello` request, records the Hello, sends back a HelloAck. Run from
// the read loop before handing off to normal frame routing — but
// implemented as a one-shot ReadFrame here so the dispatcher logic
// stays simple.
func (s *Session) handshake() error {
	if err := s.conn.SetReadDeadline(time.Now().Add(HelloTimeout)); err != nil {
		return err
	}
	env, err := ReadFrame(s.conn)
	if err != nil {
		return fmt.Errorf("appchannel: hello read: %w", err)
	}
	_ = s.conn.SetReadDeadline(time.Time{}) // clear deadline

	if env.Method != MethodHello || env.ID == 0 {
		_ = s.writeEnvelope(&Envelope{
			ID:    env.ID,
			Error: &RPCError{Code: ErrCodeInvalidRequest, Message: "first message must be hello"},
		})
		return fmt.Errorf("appchannel: expected hello, got method=%q id=%d", env.Method, env.ID)
	}
	var hello Hello
	if err := UnpackParams(env.Params, &hello); err != nil {
		_ = s.writeEnvelope(&Envelope{
			ID:    env.ID,
			Error: &RPCError{Code: ErrCodeInvalidParams, Message: "malformed hello: " + err.Error()},
		})
		return fmt.Errorf("appchannel: bad hello: %w", err)
	}

	// Intersect app's methods with spyder's known catalogue.
	known := map[string]bool{}
	for _, m := range KnownMethods {
		known[m] = true
	}
	accepted := make([]string, 0, len(hello.Methods))
	for _, m := range hello.Methods {
		if known[m] {
			accepted = append(accepted, m)
		}
	}
	ack := &HelloAck{SpyderVersion: spyderVersion, AcceptedMethods: accepted}

	s.mu.Lock()
	s.hello = &hello
	s.ack = ack
	s.mu.Unlock()

	ackRaw, _ := PackParams(ack)
	if err := s.writeEnvelope(&Envelope{ID: env.ID, Result: ackRaw}); err != nil {
		return fmt.Errorf("appchannel: hello ack write: %w", err)
	}

	slog.Info("appchannel: handshake complete",
		"session_id", s.ID, "remote", s.conn.RemoteAddr().String(),
		"app_name", hello.AppName, "app_version", hello.AppVersion,
		"app_methods", len(hello.Methods))
	return nil
}

// spyderVersion is set by the daemon at startup via SetSpyderVersion.
// Default "dev" matches the in-source placeholder so unit tests run.
var spyderVersion = "dev"

// SetSpyderVersion sets the version string returned in HelloAck. Call
// once at daemon startup.
func SetSpyderVersion(v string) { spyderVersion = v }

// Manager owns the per-session table and the listener that produces
// new sessions. One per daemon.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	keyed    map[AppKey]*Listener

	closeFn func()
}

// NewManager returns a Manager with the GC sweeper running.
func NewManager() *Manager {
	m := &Manager{
		sessions: map[string]*Session{},
		keyed:    map[AppKey]*Listener{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.closeFn = cancel
	go m.sweep(ctx)
	return m
}

// StartParams configures a new listener.
type StartParams struct {
	Owner string
	Key   AppKey // when non-zero, the listener is registered under this key
}

// Listener is the sentinel returned by GetOrCreateListener; the
// underlying TCP listener stays open until Stop is called or the
// sweeper reaps it. Each accepted connection becomes a Session.
type Listener struct {
	ID    string
	Port  int
	Owner string
	Key   AppKey // zero value when this listener is not keyed (legacy/test path)

	mgr      *Manager
	listener net.Listener
	cancel   context.CancelFunc
	done     chan struct{}

	mu          sync.Mutex
	sessions    []*Session // sessions accepted on this listener
	lastTouched time.Time
}

// GetOrCreateListener returns the keyed listener for `key`, creating
// one (and binding a kernel-assigned TCP port) on first call. The
// listener survives app crashes/relaunches — a new connection on the
// same port becomes a fresh Session under the same listener_id and
// port. The listener is reaped by the sweeper after
// `KeyedListenerIdleTTL` with no live session and no Touch.
func (m *Manager) GetOrCreateListener(key AppKey) (*Listener, error) {
	if key.DeviceID == "" || key.BundleID == "" {
		return nil, fmt.Errorf("appchannel: GetOrCreateListener: key.DeviceID and key.BundleID are required")
	}
	m.mu.Lock()
	if l, ok := m.keyed[key]; ok {
		m.mu.Unlock()
		l.Touch()
		return l, nil
	}
	m.mu.Unlock()

	l, err := m.startListener(StartParams{Key: key})
	if err != nil {
		return nil, err
	}
	// Race-resolve: another caller may have created concurrently.
	m.mu.Lock()
	if existing, ok := m.keyed[key]; ok {
		m.mu.Unlock()
		// We lost the race; tear down the duplicate.
		l.Stop()
		existing.Touch()
		return existing, nil
	}
	m.keyed[key] = l
	m.mu.Unlock()
	l.Touch()
	return l, nil
}

// LookupKeyed returns the keyed listener for `key` if one exists.
func (m *Manager) LookupKeyed(key AppKey) (*Listener, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.keyed[key]
	return l, ok
}

// KeyedListeners returns a snapshot of all keyed listeners.
func (m *Manager) KeyedListeners() []*Listener {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Listener, 0, len(m.keyed))
	for _, l := range m.keyed {
		out = append(out, l)
	}
	return out
}

// Start opens an unkeyed listener (the legacy/test path). Production
// callers should use GetOrCreateListener so the listener is tracked
// in the per-(device, bundle_id) registry.
func (m *Manager) Start(p StartParams) (*Listener, error) {
	return m.startListener(p)
}

// startListener binds a port and starts the accept loop. Internal —
// public callers go through GetOrCreateListener or Start.
func (m *Manager) startListener(p StartParams) (*Listener, error) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("appchannel: listen: %w", err)
	}
	id, err := newID()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &Listener{
		ID:          id,
		Port:        ln.Addr().(*net.TCPAddr).Port,
		Owner:       p.Owner,
		Key:         p.Key,
		mgr:         m,
		listener:    ln,
		cancel:      cancel,
		done:        make(chan struct{}),
		lastTouched: time.Now(),
	}
	go l.acceptLoop(ctx)
	slog.Info("appchannel: listener started",
		"listener_id", id, "port", l.Port, "owner", p.Owner,
		"device_id", p.Key.DeviceID, "bundle_id", p.Key.BundleID)
	return l, nil
}

// Touch updates the listener's last-activity timestamp; the idle reaper
// uses this to decide whether a listener with zero live sessions has
// been quiet long enough to drop.
func (l *Listener) Touch() {
	l.mu.Lock()
	l.lastTouched = time.Now()
	l.mu.Unlock()
}

// LastTouched returns the listener's last-activity timestamp.
func (l *Listener) LastTouched() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastTouched
}

// Stop closes the listener and all sessions accepted on it. If the
// listener was keyed, it is also removed from the manager's keyed
// registry.
func (l *Listener) Stop() {
	l.cancel()
	_ = l.listener.Close()
	<-l.done

	l.mu.Lock()
	sessions := l.sessions
	l.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}

	l.mgr.mu.Lock()
	for _, s := range sessions {
		delete(l.mgr.sessions, s.ID)
	}
	if l.Key != (AppKey{}) {
		if existing, ok := l.mgr.keyed[l.Key]; ok && existing == l {
			delete(l.mgr.keyed, l.Key)
		}
	}
	l.mgr.mu.Unlock()
}

// Sessions returns a snapshot of accepted sessions on this listener.
func (l *Listener) Sessions() []*Session {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*Session, len(l.sessions))
	copy(out, l.sessions)
	return out
}

func (l *Listener) acceptLoop(ctx context.Context) {
	defer close(l.done)
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("appchannel: accept error", "listener_id", l.ID, "error", err)
			}
			return
		}
		go l.handleConn(ctx, conn)
	}
}

func (l *Listener) handleConn(ctx context.Context, conn net.Conn) {
	id, err := newID()
	if err != nil {
		_ = conn.Close()
		return
	}
	sCtx, sCancel := context.WithCancel(ctx)
	s := &Session{
		ID:            id,
		Port:          l.Port,
		Owner:         l.Owner,
		StartedAt:     time.Now(),
		listener:      l,
		conn:          conn,
		cancel:        sCancel,
		done:          make(chan struct{}),
		pending:       map[uint64]*pending{},
		stateCaptures: map[string]*StateCapture{},
	}

	if err := s.handshake(); err != nil {
		slog.Warn("appchannel: handshake failed",
			"session_id", id, "remote", conn.RemoteAddr().String(), "error", err)
		_ = conn.Close()
		return
	}

	l.mu.Lock()
	l.sessions = append(l.sessions, s)
	l.lastTouched = time.Now()
	l.mu.Unlock()
	l.mgr.mu.Lock()
	l.mgr.sessions[id] = s
	l.mgr.mu.Unlock()

	s.readLoop(sCtx)

	// Session ended; reap from manager and from this listener's
	// session list. Bump lastTouched so the idle reaper starts the
	// TTL clock from the disconnect.
	l.mgr.mu.Lock()
	delete(l.mgr.sessions, id)
	l.mgr.mu.Unlock()
	l.mu.Lock()
	for i, ls := range l.sessions {
		if ls == s {
			l.sessions = append(l.sessions[:i], l.sessions[i+1:]...)
			break
		}
	}
	l.lastTouched = time.Now()
	l.mu.Unlock()
}

// GetSession returns the session by ID, if any.
func (m *Manager) GetSession(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Sessions returns a snapshot of every live session across all listeners.
func (m *Manager) Sessions() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// Close shuts down the sweeper. Listeners must be stopped individually.
func (m *Manager) Close() {
	m.closeFn()
}

func (m *Manager) sweep(ctx context.Context) {
	t := time.NewTicker(KeyedListenerSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ReapIdleKeyedListeners()
		}
	}
}

// ReapIdleKeyedListeners stops and removes any keyed listener that
// has no live session and whose lastTouched is older than
// KeyedListenerIdleTTL. Called by the background sweeper; exported
// so tests (and operational tooling) can force a synchronous pass.
func (m *Manager) ReapIdleKeyedListeners() {
	m.mu.Lock()
	candidates := make([]*Listener, 0, len(m.keyed))
	for _, l := range m.keyed {
		candidates = append(candidates, l)
	}
	m.mu.Unlock()

	cutoff := time.Now().Add(-KeyedListenerIdleTTL)
	for _, l := range candidates {
		l.mu.Lock()
		live := len(l.sessions)
		last := l.lastTouched
		l.mu.Unlock()
		if live > 0 {
			continue
		}
		if last.After(cutoff) {
			continue
		}
		slog.Info("appchannel: reaping idle keyed listener",
			"listener_id", l.ID, "port", l.Port,
			"device_id", l.Key.DeviceID, "bundle_id", l.Key.BundleID,
			"idle_for", time.Since(last).Round(time.Second).String())
		l.Stop()
	}
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
