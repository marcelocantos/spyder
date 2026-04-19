// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package reservations tracks exclusive-use holds on devices so
// parallel dev sessions don't yank each other's device state.
//
// A reservation is a {device, owner, expires_at, note} tuple. Only
// the owner can release or renew. Other mutating operations against
// the same device fail with a Conflict while the reservation is live.
// Read-only operations are unaffected.
//
// Device identity is normalized through a caller-supplied Normalizer
// (typically the inventory alias) so reserving "Pippa" also blocks
// operations against Pippa's raw UDID and vice versa.
package reservations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultTTL is the TTL applied when a caller passes 0.
const DefaultTTL = time.Hour

// MaxTTL is the soft cap applied to Acquire/Renew.
const MaxTTL = 24 * time.Hour

// Reservation describes a live device hold.
type Reservation struct {
	Device    string    `json:"device"` // canonical form (alias if known)
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Normalizer maps a user-supplied device reference (alias or raw UUID)
// to a canonical form used as the reservation key.
type Normalizer func(ref string) string

// Store is a concurrent-safe reservation book with file-backed
// persistence.
type Store struct {
	mu    sync.Mutex
	path  string
	now   func() time.Time
	norm  Normalizer
	state map[string]Reservation // canonical device → reservation
}

// Option configures a Store at construction.
type Option func(*Store)

// WithNow injects a clock. Defaults to time.Now.
func WithNow(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithNormalizer injects a device-name normalizer. Defaults to identity.
func WithNormalizer(n Normalizer) Option { return func(s *Store) { s.norm = n } }

// New opens (or creates) a Store backed by the given file path.
// Passing an empty path makes the store in-memory only — useful for
// tests. Expired entries in the file are pruned on load.
func New(path string, opts ...Option) (*Store, error) {
	s := &Store{
		path:  path,
		now:   time.Now,
		norm:  func(ref string) string { return ref },
		state: map[string]Reservation{},
	}
	for _, o := range opts {
		o(s)
	}
	if path != "" {
		if err := s.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("load reservations: %w", err)
		}
	}
	return s, nil
}

// Conflict is returned when a caller tries to acquire, modify, or act
// against a device that's already reserved by someone else.
type Conflict struct {
	Reservation Reservation
}

// Error implements error. Text includes the holder, expiry, and note
// for human-friendly resolution.
func (c *Conflict) Error() string {
	msg := fmt.Sprintf("device %s is reserved by %s until %s",
		c.Reservation.Device, c.Reservation.Owner,
		c.Reservation.ExpiresAt.Format(time.RFC3339))
	if c.Reservation.Note != "" {
		msg += fmt.Sprintf(" (note: %q)", c.Reservation.Note)
	}
	return msg
}

// IsConflict reports whether err is a reservation Conflict.
func IsConflict(err error) bool {
	var c *Conflict
	return errors.As(err, &c)
}

// Acquire places a new reservation on device. Returns a Conflict if
// the device is already held by someone else. If the caller is the
// existing holder, the existing reservation is renewed with the new
// TTL/note.
func (s *Store) Acquire(device, owner string, ttl time.Duration, note string) (Reservation, error) {
	if owner == "" {
		return Reservation{}, errors.New("owner is required")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.canonical(device)
	now := s.now()
	if existing, ok := s.state[key]; ok && now.Before(existing.ExpiresAt) {
		if existing.Owner != owner {
			return Reservation{}, &Conflict{Reservation: existing}
		}
		// Same owner — upgrade note + expiry.
		existing.ExpiresAt = now.Add(ttl)
		if note != "" {
			existing.Note = note
		}
		s.state[key] = existing
		_ = s.saveLocked()
		return existing, nil
	}
	r := Reservation{
		Device:    key,
		Owner:     owner,
		ExpiresAt: now.Add(ttl),
		Note:      note,
		CreatedAt: now,
	}
	s.state[key] = r
	_ = s.saveLocked()
	return r, nil
}

// Release frees a reservation. Only the holder may release (or any
// caller if the reservation has already expired). Releasing a free
// device is a no-op (returns nil).
func (s *Store) Release(device, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.canonical(device)
	existing, ok := s.state[key]
	if !ok {
		return nil
	}
	if s.now().After(existing.ExpiresAt) {
		delete(s.state, key)
		_ = s.saveLocked()
		return nil
	}
	if existing.Owner != owner {
		return &Conflict{Reservation: existing}
	}
	delete(s.state, key)
	_ = s.saveLocked()
	return nil
}

// Renew extends the TTL on an existing reservation. Only the owner
// may renew. Returns a Conflict if another owner holds it.
func (s *Store) Renew(device, owner string, ttl time.Duration) (Reservation, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.canonical(device)
	existing, ok := s.state[key]
	if !ok || s.now().After(existing.ExpiresAt) {
		return Reservation{}, fmt.Errorf("no active reservation on %s", key)
	}
	if existing.Owner != owner {
		return Reservation{}, &Conflict{Reservation: existing}
	}
	existing.ExpiresAt = s.now().Add(ttl)
	s.state[key] = existing
	_ = s.saveLocked()
	return existing, nil
}

// Authorize returns nil if the device is free, the owner matches the
// current holder, or the current holder's reservation has expired.
// Otherwise returns a *Conflict. Callers pass "" for owner to
// represent an anonymous caller (rejected if anyone holds it).
func (s *Store) Authorize(device, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.canonical(device)
	existing, ok := s.state[key]
	if !ok {
		return nil
	}
	if s.now().After(existing.ExpiresAt) {
		delete(s.state, key)
		_ = s.saveLocked()
		return nil
	}
	if existing.Owner == owner {
		return nil
	}
	return &Conflict{Reservation: existing}
}

// List returns active (unexpired) reservations. Filters out expired
// entries as a side effect; persists the pruned state.
func (s *Store) List() []Reservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

// Get returns the active reservation for a device, if any.
func (s *Store) Get(device string) (Reservation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.canonical(device)
	r, ok := s.state[key]
	if !ok {
		return Reservation{}, false
	}
	if s.now().After(r.ExpiresAt) {
		delete(s.state, key)
		_ = s.saveLocked()
		return Reservation{}, false
	}
	return r, true
}

func (s *Store) canonical(ref string) string {
	if s.norm == nil {
		return ref
	}
	if n := s.norm(ref); n != "" {
		return n
	}
	return ref
}

func (s *Store) listLocked() []Reservation {
	now := s.now()
	dirty := false
	out := make([]Reservation, 0, len(s.state))
	for k, r := range s.state {
		if now.After(r.ExpiresAt) {
			delete(s.state, k)
			dirty = true
			continue
		}
		out = append(out, r)
	}
	if dirty {
		_ = s.saveLocked()
	}
	return out
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var entries []Reservation
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parsing reservations: %w", err)
	}
	now := s.now()
	for _, r := range entries {
		if now.Before(r.ExpiresAt) {
			s.state[r.Device] = r
		}
	}
	return nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	entries := make([]Reservation, 0, len(s.state))
	for _, r := range s.state {
		entries = append(entries, r)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: tmp + rename so a concurrent reader never sees a
	// half-written file. Both the daemon and `spyder run` may be
	// writing; atomic-rename keeps each individual write coherent
	// even if one is the eventual loser of a race.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
