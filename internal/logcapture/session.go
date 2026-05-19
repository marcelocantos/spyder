// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package logcapture runs server-managed log-capture sessions on top
// of [device.Adapter.LogStream]. A session is a long-running capture
// against a single device, identified by a server-assigned session_id,
// with a bounded in-memory buffer that fills as log lines arrive and
// drains when the agent calls Get (peek) or Stop (drain + tear down).
//
// The model replaces the agent-side `spyder log --follow > /tmp/cap
// &; kill <pid>` dance: the agent never touches shell or file paths,
// the server owns the lifecycle, and a session can be queried
// incrementally without stopping it. Sessions auto-expire after a
// configurable TTL so a forgotten capture doesn't pin device IO
// indefinitely. (🎯T60.)
//
// Buffer policy: each session has a bounded ring (configurable max
// bytes AND max line count, whichever fires first). When full, the
// oldest entry is evicted FIFO and a per-session counter tracks how
// many lines have been dropped. Get and Stop return the count so the
// caller can tell when their capture wasn't lossless.
//
// Concurrency: Manager is safe for concurrent use. Each session runs
// one consumer goroutine that drains the adapter's LogStream channel
// into the ring buffer under a per-session mutex.
package logcapture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

const (
	// DefaultTTL bounds an unstopped session's lifetime. After this
	// elapses with no Get or Stop call, the session is auto-torn-down
	// and its buffer freed.
	DefaultTTL = 5 * time.Minute

	// MaxTTL caps the user-supplied TTL. Lets the daemon refuse
	// "stay alive forever" requests.
	MaxTTL = 24 * time.Hour

	// DefaultMaxBytes bounds a session's buffer at ~50 MB of accumulated
	// message payload. The accounting is approximate (per-line overhead
	// from struct headers is not counted) — close enough to keep a
	// runaway capture from eating gigabytes.
	DefaultMaxBytes = 50 * 1024 * 1024

	// DefaultMaxLines bounds a session at 100k lines regardless of
	// per-line size. A complementary guard against the byte budget
	// in case payloads are short and numerous.
	DefaultMaxLines = 100_000

	// sweepInterval is the cadence for the TTL-expiry sweeper.
	sweepInterval = 30 * time.Second
)

// Session is an active capture. Constructed by Manager.Start;
// owned by the manager until Stop or TTL expiry.
type Session struct {
	ID        string
	Device    string // user-facing device label (alias or UUID)
	DeviceID  string // platform-specific id resolved at Start time
	Owner     string
	Filter    device.LogFilter
	StartedAt time.Time
	TTL       time.Duration
	MaxBytes  int
	MaxLines  int

	// cancel terminates the consumer goroutine.
	cancel context.CancelFunc
	// done fires when the consumer goroutine has exited.
	done chan struct{}

	// mu guards everything below.
	mu       sync.Mutex
	buf      []device.LogLine
	bufBytes int
	dropped  int
	lastUsed time.Time
}

// approxLineBytes is the conservative per-line accounting used to
// enforce MaxBytes. Counts the message string only — close enough for
// budget-firing decisions, far cheaper than reflect-based introspection.
func approxLineBytes(ll device.LogLine) int {
	return len(ll.Message) + len(ll.Process) + len(ll.Level) + len(ll.Tag) + 24 // Timestamp + slop
}

// append adds a line, evicting FIFO until the buffer fits both bounds.
// Returns the number of newly-dropped lines.
func (s *Session) append(ll device.LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sz := approxLineBytes(ll)
	s.buf = append(s.buf, ll)
	s.bufBytes += sz

	for len(s.buf) > s.MaxLines || s.bufBytes > s.MaxBytes {
		evicted := s.buf[0]
		s.bufBytes -= approxLineBytes(evicted)
		s.buf = s.buf[1:]
		s.dropped++
	}
	s.lastUsed = time.Now()
}

// drain returns the buffered lines and resets the buffer. dropped
// counter is also returned and reset — the caller learns about
// eviction since the last drain, not since the session started.
// The returned slice is always non-nil so JSON marshalling produces
// `[]` for an empty drain rather than `null`.
func (s *Session) drain() (lines []device.LogLine, dropped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf == nil {
		lines = []device.LogLine{}
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

// snapshot returns a copy of the buffered lines without clearing.
// Used by Manager's drain-on-shutdown path.
func (s *Session) snapshot() ([]device.LogLine, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]device.LogLine, len(s.buf))
	copy(out, s.buf)
	return out, s.dropped
}

// idle reports whether the session has been inactive for at least its
// TTL. Used by the sweeper.
func (s *Session) idle(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.lastUsed) >= s.TTL
}

// Adapter is the subset of [device.Adapter] that a Manager needs.
// Carved out for test fakes that don't drag in a real iOS / Android
// stack.
type Adapter interface {
	LogStream(ctx context.Context, id string, filter device.LogFilter, out chan<- device.LogLine) error
}

// Manager owns the session table. One manager per daemon. Construct
// with NewManager and call Close on daemon shutdown.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session

	closed  chan struct{}
	closeFn func() // sweeper teardown
}

// NewManager returns a Manager with a background TTL sweeper running.
// Call Close to stop the sweeper and tear down all live sessions.
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

// StartParams are the inputs to Manager.Start.
type StartParams struct {
	Device   string
	DeviceID string
	Filter   device.LogFilter
	Owner    string
	TTL      time.Duration // 0 → DefaultTTL
	MaxBytes int           // 0 → DefaultMaxBytes
	MaxLines int           // 0 → DefaultMaxLines
}

// Start launches a new session that drains adapter.LogStream into a
// bounded ring buffer until Stop is called or TTL fires. Returns the
// new session.
func (m *Manager) Start(ctx context.Context, ad Adapter, p StartParams) (*Session, error) {
	if ad == nil {
		return nil, errors.New("logcapture: adapter is required")
	}
	if p.DeviceID == "" {
		return nil, errors.New("logcapture: device_id is required")
	}
	ttl := p.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		return nil, fmt.Errorf("logcapture: ttl %s exceeds max %s", ttl, MaxTTL)
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
		return nil, fmt.Errorf("logcapture: id: %w", err)
	}

	streamCtx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:        id,
		Device:    p.Device,
		DeviceID:  p.DeviceID,
		Owner:     p.Owner,
		Filter:    p.Filter,
		StartedAt: time.Now(),
		TTL:       ttl,
		MaxBytes:  maxBytes,
		MaxLines:  maxLines,
		cancel:    cancel,
		done:      make(chan struct{}),
		lastUsed:  time.Now(),
	}

	// Buffered so the adapter can outrun the consumer briefly without
	// blocking inside the device read loop. 256 is big enough to soak
	// up burst arrivals at the standard DTX flush rate (~500ms).
	ch := make(chan device.LogLine, 256)

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go func() {
		err := ad.LogStream(streamCtx, p.DeviceID, p.Filter, ch)
		close(ch)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("logcapture: LogStream ended with error",
				"session_id", id, "device_id", p.DeviceID, "error", err)
		}
	}()

	go func() {
		defer close(s.done)
		for ll := range ch {
			s.append(ll)
		}
	}()

	slog.Info("logcapture: session started",
		"session_id", id, "device", p.Device, "owner", p.Owner,
		"ttl", ttl, "max_bytes", maxBytes, "max_lines", maxLines)

	return s, nil
}

// Get returns the currently-buffered lines for a session without
// stopping it. Calling Get clears the session's buffer; subsequent
// Get / Stop calls only see lines arriving after this point.
func (m *Manager) Get(id string) (*GetResult, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("logcapture: no such session: %s", id)
	}
	lines, dropped := s.drain()
	return &GetResult{
		SessionID:    id,
		CapturedAt:   time.Now(),
		Lines:        lines,
		DroppedLines: dropped,
	}, nil
}

// Stop returns the currently-buffered lines for a session, then tears
// the session down. Idempotent in the no-such-session sense: a second
// Stop on the same id returns an error.
func (m *Manager) Stop(id string) (*StopResult, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("logcapture: no such session: %s", id)
	}
	s.cancel()
	<-s.done
	lines, dropped := s.drain()
	slog.Info("logcapture: session stopped",
		"session_id", id, "lines", len(lines), "dropped", dropped)
	return &StopResult{
		SessionID:    id,
		StoppedAt:    time.Now(),
		Lines:        lines,
		DroppedLines: dropped,
	}, nil
}

// List returns metadata for every live session. Sort order is by
// StartedAt ascending so the oldest session lists first.
func (m *Manager) List() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		out = append(out, SessionInfo{
			SessionID:   s.ID,
			Device:      s.Device,
			Owner:       s.Owner,
			StartedAt:   s.StartedAt,
			ExpiresAt:   s.lastUsed.Add(s.TTL),
			BufferLines: len(s.buf),
			BufferBytes: s.bufBytes,
			Dropped:     s.dropped,
			Filter:      s.Filter,
		})
		s.mu.Unlock()
	}
	return out
}

// Close terminates every live session and stops the TTL sweeper. Safe
// to call at most once per Manager.
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

// sweep runs every sweepInterval, terminating sessions whose lastUsed
// is older than their TTL. The sweeper is the only path that fires
// auto-expiry; consumers always go through Get or Stop to read.
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
				slog.Info("logcapture: session expired by TTL", "session_id", id)
				_, _ = m.Stop(id)
			}
		}
	}
}

// GetResult is the payload returned by Manager.Get.
type GetResult struct {
	SessionID    string            `json:"session_id"`
	CapturedAt   time.Time         `json:"captured_at"`
	Lines        []device.LogLine  `json:"lines"`
	DroppedLines int               `json:"dropped_lines,omitempty"`
}

// StopResult is the payload returned by Manager.Stop.
type StopResult struct {
	SessionID    string            `json:"session_id"`
	StoppedAt    time.Time         `json:"stopped_at"`
	Lines        []device.LogLine  `json:"lines"`
	DroppedLines int               `json:"dropped_lines,omitempty"`
}

// SessionInfo is the per-session record returned by Manager.List.
type SessionInfo struct {
	SessionID   string           `json:"session_id"`
	Device      string           `json:"device"`
	Owner       string           `json:"owner,omitempty"`
	StartedAt   time.Time        `json:"started_at"`
	ExpiresAt   time.Time        `json:"expires_at"`
	BufferLines int              `json:"buffer_lines"`
	BufferBytes int              `json:"buffer_bytes"`
	Dropped     int              `json:"dropped_lines,omitempty"`
	Filter      device.LogFilter `json:"filter"`
}

// newID returns a URL-safe random session id, 16 hex digits (64 bits
// of entropy). Conflicts are not retried — the chance of a collision
// across the lifetime of a single daemon is negligible.
func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
