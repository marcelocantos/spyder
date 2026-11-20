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
// (typically the inventory alias) so reserving "iPad" also blocks
// operations against iPad's raw UDID and vice versa.
package reservations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
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
// persistence. The file is the authority: every grant, renewal,
// release and read reloads it under an exclusive lock, then replaces
// it by renaming a private temp file. A second process — the daemon
// and `spyder run` each keep a Store — therefore cannot grant from a
// snapshot taken at open, and a crash mid-write leaves the previous
// book in place.
type Store struct {
	onChange func(string, Reservation)
	mu       sync.Mutex
	path     string
	lock     *os.File
	now      func() time.Time
	norm     Normalizer
	state    map[string]Reservation // canonical device → reservation
	pending  []bookNote
}

// bookNote is a lifecycle event recorded during a mutation and fired
// only after the book has been replaced.
type bookNote struct {
	kind string
	res  Reservation
}

// CorruptBookError means the book on disk is not a reservation list.
// Callers move it aside and open an empty book; they do not turn
// enforcement off.
type CorruptBookError struct {
	Path string
	Err  error
}

func (e *CorruptBookError) Error() string {
	return fmt.Sprintf("parsing reservations %s: %v", e.Path, e.Err)
}

func (e *CorruptBookError) Unwrap() error { return e.Err }

// Option configures a Store at construction.
type Option func(*Store)

// SetOnChange installs a synchronous lifecycle observer. It runs under the
// store lock and must not re-enter the store or perform network I/O.
func (s *Store) SetOnChange(fn func(string, Reservation)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}
func (s *Store) changedLocked(kind string, r Reservation) {
	if s.onChange != nil {
		s.onChange(kind, r)
	}
}

// WithNow injects a clock. Defaults to time.Now.
func WithNow(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithNormalizer injects a device-name normalizer. Defaults to identity.
func WithNormalizer(n Normalizer) Option { return func(s *Store) { s.norm = n } }

// New opens (or creates) a Store backed by the given file path.
// Passing an empty path makes the store in-memory only — useful for
// tests. Expired entries stay in the file until a later operation
// observes them; opening the book does not rewrite it.
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

// OpenBook opens the book at path. When the file is corrupt it is moved
// aside and the returned store is an empty enforcing book. quarantined
// is the path of the moved file, or empty when the book was readable.
func OpenBook(path string, opts ...Option) (s *Store, quarantined string, err error) {
	s, err = New(path, opts...)
	var corrupt *CorruptBookError
	if !errors.As(err, &corrupt) {
		return s, "", err
	}
	moved, qerr := quarantineBook(path)
	if qerr != nil {
		return nil, "", fmt.Errorf("quarantine corrupt reservations: %w", qerr)
	}
	s, err = New(path, opts...)
	if err != nil {
		return nil, moved, err
	}
	return s, moved, nil
}

func quarantineBook(path string) (string, error) {
	dest := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(path, dest); err != nil {
		return "", err
	}
	return dest, nil
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

// reservationLive is the one rule for "this hold still excludes other
// owners". The expiry instant itself is not live.
func reservationLive(now time.Time, r Reservation) bool {
	return now.Before(r.ExpiresAt)
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

	// Normalise before locking: the normaliser consults the inventory,
	// which may touch the filesystem (🎯T168).
	key := s.canonical(device)
	var out Reservation
	err := s.withBook(func() error {
		now := s.now()
		if existing, ok := s.state[key]; ok && reservationLive(now, existing) {
			if existing.Owner != owner {
				return &Conflict{Reservation: existing}
			}
			existing.ExpiresAt = now.Add(ttl)
			if note != "" {
				existing.Note = note
			}
			s.state[key] = existing
			s.note("renewed", existing)
			slog.Info("reservation renewed (acquire by same owner)",
				"device", key, "owner", owner, "ttl", ttl)
			out = existing
			return nil
		}
		if expired, ok := s.state[key]; ok {
			s.note("expired", expired)
		}
		r := Reservation{
			Device:    key,
			Owner:     owner,
			ExpiresAt: now.Add(ttl),
			Note:      note,
			CreatedAt: now,
		}
		s.state[key] = r
		s.note("acquired", r)
		slog.Info("reservation acquired",
			"device", key, "owner", owner, "ttl", ttl, "note", note)
		out = r
		return nil
	})
	return out, err
}

// Release frees a reservation. Only the holder may release (or any
// caller if the reservation has already expired). Releasing a free
// device is a no-op (returns nil).
func (s *Store) Release(device, owner string) error {
	// Normalise before locking: the normaliser consults the inventory,
	// which may touch the filesystem (🎯T168).
	key := s.canonical(device)
	return s.withBook(func() error {
		existing, ok := s.state[key]
		if !ok {
			return nil
		}
		if !reservationLive(s.now(), existing) {
			delete(s.state, key)
			s.note("expired", existing)
			slog.Info("reservation released (already expired)",
				"device", key, "prev_owner", existing.Owner)
			return nil
		}
		if existing.Owner != owner {
			return &Conflict{Reservation: existing}
		}
		delete(s.state, key)
		s.note("released", existing)
		slog.Info("reservation released", "device", key, "owner", owner)
		return nil
	})
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

	// Normalise before locking: the normaliser consults the inventory,
	// which may touch the filesystem (🎯T168).
	key := s.canonical(device)
	var out Reservation
	err := s.withBook(func() error {
		existing, ok := s.state[key]
		if !ok || !reservationLive(s.now(), existing) {
			return fmt.Errorf("no active reservation on %s", key)
		}
		if existing.Owner != owner {
			return &Conflict{Reservation: existing}
		}
		existing.ExpiresAt = s.now().Add(ttl)
		s.state[key] = existing
		s.note("renewed", existing)
		slog.Info("reservation renewed",
			"device", key, "owner", owner, "ttl", ttl)
		out = existing
		return nil
	})
	return out, err
}

// Authorize returns nil if the device is free, the owner matches the
// current holder, or the current holder's reservation has expired.
// Otherwise returns a *Conflict. Callers pass "" for owner to
// represent an anonymous caller (rejected if anyone holds it).
func (s *Store) Authorize(device, owner string) error {
	// Normalise before locking: the normaliser consults the inventory,
	// which may touch the filesystem (🎯T168).
	key := s.canonical(device)
	return s.withBook(func() error {
		existing, ok := s.state[key]
		if !ok {
			return nil
		}
		if !reservationLive(s.now(), existing) {
			delete(s.state, key)
			s.note("expired", existing)
			return nil
		}
		if existing.Owner == owner {
			return nil
		}
		return &Conflict{Reservation: existing}
	})
}

// List returns active (unexpired) reservations. Filters out expired
// entries as a side effect; persists the pruned state.
func (s *Store) List() []Reservation {
	var out []Reservation
	if err := s.withBook(func() error {
		out = s.listInBook()
		return nil
	}); err != nil {
		slog.Error("reservation list", "error", err)
		return nil
	}
	return out
}

// Get returns the active reservation for a device, if any.
func (s *Store) Get(device string) (Reservation, bool) {
	key := s.canonical(device) // before locking (🎯T168)
	var r Reservation
	var ok bool
	if err := s.withBook(func() error {
		existing, found := s.state[key]
		if !found {
			return nil
		}
		if !reservationLive(s.now(), existing) {
			delete(s.state, key)
			s.note("expired", existing)
			return nil
		}
		r, ok = existing, true
		return nil
	}); err != nil {
		slog.Error("reservation get", "device", key, "error", err)
		return Reservation{}, false
	}
	return r, ok
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

func (s *Store) listInBook() []Reservation {
	now := s.now()
	out := make([]Reservation, 0, len(s.state))
	for k, r := range s.state {
		if !reservationLive(now, r) {
			delete(s.state, k)
			s.note("expired", r)
			continue
		}
		out = append(out, r)
	}
	return out
}

// withBook reloads the book under the process lock, runs fn, and
// replaces the file when the map changed. fn's error leaves the
// previous book untouched. Lifecycle notes fire only after a
// successful replace.
func (s *Store) withBook(fn func() error) error {
	s.mu.Lock()
	if err := s.lockFile(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.reloadLocked(); err != nil {
		s.unlockFile()
		s.mu.Unlock()
		return err
	}
	prev := cloneBook(s.state)
	err := fn()
	if err != nil {
		s.state = prev
		s.pending = nil
		s.unlockFile()
		s.mu.Unlock()
		return err
	}
	if !sameBook(prev, s.state) {
		if err := s.saveLocked(); err != nil {
			s.state = prev
			s.pending = nil
			s.unlockFile()
			s.mu.Unlock()
			return err
		}
	}
	pending := s.pending
	s.pending = nil
	for _, n := range pending {
		s.changedLocked(n.kind, n.res)
	}
	s.unlockFile()
	s.mu.Unlock()
	return nil
}

func (s *Store) note(kind string, r Reservation) {
	s.pending = append(s.pending, bookNote{kind: kind, res: r})
}

func (s *Store) lockFile() error {
	if s.path == "" {
		return nil
	}
	if s.lock == nil {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		s.lock = f
	}
	return syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX)
}

func (s *Store) unlockFile() {
	if s.lock == nil {
		return
	}
	if err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN); err != nil {
		slog.Error("reservation unlock", "error", err)
	}
}

func (s *Store) reloadLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = map[string]Reservation{}
		return nil
	}
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		s.state = map[string]Reservation{}
		return nil
	}
	next, err := parseBook(s.path, data)
	if err != nil {
		return err
	}
	s.state = next
	return nil
}

func parseBook(path string, data []byte) (map[string]Reservation, error) {
	var entries []Reservation
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, &CorruptBookError{Path: path, Err: err}
	}
	next := make(map[string]Reservation, len(entries))
	for _, r := range entries {
		if r.Device == "" {
			continue
		}
		next[r.Device] = r
	}
	return next, nil
}

func cloneBook(in map[string]Reservation) map[string]Reservation {
	out := make(map[string]Reservation, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sameBook(a, b map[string]Reservation) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || av != bv {
			return false
		}
	}
	return true
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		s.state = map[string]Reservation{}
		return nil
	}
	next, err := parseBook(s.path, data)
	if err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries := make([]Reservation, 0, len(s.state))
	for _, r := range s.state {
		entries = append(entries, r)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Device < entries[j].Device })
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// A private temp file, synced, then renamed. Two writers no longer
	// share "<path>.tmp", and a crash before the rename leaves the
	// previous book as the authority.
	tmp, err := os.CreateTemp(dir, ".reservations-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if testHookBeforeRename != nil {
		testHookBeforeRename()
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	keep = true
	dirf, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer dirf.Close()
	if err := dirf.Sync(); err != nil {
		slog.Error("reservations directory sync", "path", dir, "error", err)
	}
	return nil
}

// testHookBeforeRename runs after the new book is staged and before it
// replaces the previous one. Tests use it to die in that window.
var testHookBeforeRename func()
