// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package recording manages in-process screen-recording sessions.
// Each session tracks a long-running recorder subprocess for a single
// (device, owner) pair. The registry is safe for concurrent use.
//
// Only one active recorder per device is permitted at a time. Attempting
// to start a second recorder on the same device while one is running
// returns ErrConflict naming the current holder.
package recording

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// ErrConflict is returned when StartRecording is called on a device
// that already has an active recording session.
type ErrConflict struct {
	Device       string
	CurrentOwner string
}

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("device %q is already being recorded by owner %q", e.Device, e.CurrentOwner)
}

// IsConflict reports whether err is an ErrConflict.
func IsConflict(err error) bool {
	var c *ErrConflict
	return errors.As(err, &c)
}

// Session holds state for one active recording session.
type Session struct {
	Device     string
	Owner      string
	OutputPath string // where the mp4 will land
	StartTime  time.Time

	// stopFn is called by Stop to signal the recorder to terminate.
	// It must be safe to call from any goroutine.
	stopFn func() error

	// doneCh is closed when the recorder subprocess has exited and
	// the output file is ready.
	doneCh <-chan struct{}
}

// Done returns a channel that is closed once the recorder has fully
// exited and the output file is finalised. Callers may select on it
// or check len(Done()) == 0.
func (s *Session) Done() <-chan struct{} { return s.doneCh }

// Registry tracks active recording sessions. Zero value is not safe;
// use NewRegistry.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*Session // keyed by canonical device id
}

// NewRegistry returns an initialised Registry.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

// Start registers a new recording session for device. If device already
// has an active session, ErrConflict is returned immediately.
//
// outputPath is the full path the caller intends the mp4 to land at.
// stopFn is called by Stop to signal the recorder subprocess (e.g. send
// SIGINT). doneCh must be closed once the subprocess exits and the
// output file is ready.
func (r *Registry) Start(device, owner, outputPath string, stopFn func() error, doneCh <-chan struct{}) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.sessions[device]; ok {
		return nil, &ErrConflict{Device: device, CurrentOwner: existing.Owner}
	}
	s := &Session{
		Device:     device,
		Owner:      owner,
		OutputPath: outputPath,
		StartTime:  time.Now(),
		stopFn:     stopFn,
		doneCh:     doneCh,
	}
	r.sessions[device] = s
	return s, nil
}

// Get returns the active session for device, or (nil, nil) if none.
func (r *Registry) Get(device string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[device]
	if !ok {
		return nil, nil
	}
	return s, nil
}

// Stop signals the recorder to stop and removes it from the registry.
// It does NOT wait for the subprocess to exit — the caller should wait
// on s.Done() for that. Returns ErrNotRecording if no session exists.
// The session is removed from the registry whether stopFn succeeds or
// not so a failed stop doesn't permanently block the device.
func (r *Registry) Stop(device string) (*Session, error) {
	r.mu.Lock()
	s, ok := r.sessions[device]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("no active recording session on %q", device)
	}
	delete(r.sessions, device)
	r.mu.Unlock()

	if err := s.stopFn(); err != nil {
		// Best-effort: the subprocess may have already exited.
		return s, nil
	}
	return s, nil
}

// ForOwner returns all active sessions owned by owner. Thread-safe.
func (r *Registry) ForOwner(owner string) []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Session
	for _, s := range r.sessions {
		if s.Owner == owner {
			out = append(out, s)
		}
	}
	return out
}

// ForDevice returns the active session for device, or nil. Same as Get
// but without the error return, for use in defer-path cleanup.
func (r *Registry) ForDevice(device string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[device]
}

// Remove drops the session from the registry without sending stop signal.
// Used internally when the subprocess has already exited on its own.
func (r *Registry) Remove(device string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, device)
}

// MakeTempFile creates a temporary file in dir with the given suffix.
// The caller is responsible for closing and eventually removing or
// promoting (renaming) the file. dir must already exist.
func MakeTempFile(dir, suffix string) (*os.File, error) {
	f, err := os.CreateTemp(dir, "rec-*"+suffix)
	if err != nil {
		return nil, fmt.Errorf("recording: create temp file in %s: %w", dir, err)
	}
	// Close immediately; the recorder subprocess will write to the path.
	if err := f.Close(); err != nil {
		return nil, err
	}
	return f, nil
}
