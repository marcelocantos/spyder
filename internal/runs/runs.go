// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package runs manages per-reservation artefact directories under
// ~/.spyder/runs/<run-id>/. Each run holds the screenshots, recordings,
// logs, and crash reports produced during a single reserved session,
// plus a manifest.json enumerating them.
//
// The filesystem is the source of truth: each run dir has its own
// manifest.json; the Store is a thin wrapper that enumerates dirs and
// reads/writes manifests atomically. Multiple processes (the daemon
// and `spyder run`) can share the same base dir safely as long as
// they pick distinct run-ids (mint via Open).
package runs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Policy controls run-dir retention. Zero values disable the
// corresponding bound.
type Policy struct {
	MaxAge  time.Duration // prune runs older than MaxAge (based on CreatedAt)
	MaxSize int64         // prune oldest runs until total bytes ≤ MaxSize
}

// Run is the in-memory form of a run dir's manifest.
type Run struct {
	ID        string     `json:"id"`
	Device    string     `json:"device"`
	Owner     string     `json:"owner"`
	Note      string     `json:"note,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	Artefacts []Artefact `json:"artefacts,omitempty"`
}

// Artefact is one file produced by a spyder tool during a run.
type Artefact struct {
	Name      string    `json:"name"`
	Source    string    `json:"source"` // tool name (e.g. "screenshot")
	MIMEType  string    `json:"mime_type,omitempty"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// Option configures a Store at construction.
type Option func(*Store)

// WithNow injects a clock (test hook). Defaults to time.Now.
func WithNow(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithPolicy installs a retention policy.
func WithPolicy(p Policy) Option { return func(s *Store) { s.policy = p } }

// Store is the top-level artefact-store handle. Safe for concurrent
// use within one process; the filesystem protocol tolerates multiple
// processes as long as each picks a distinct run-id.
type Store struct {
	mu      sync.Mutex
	baseDir string
	now     func() time.Time
	policy  Policy
}

// New opens a Store rooted at baseDir. Creates the dir on demand;
// doesn't fail if it doesn't exist yet.
func New(baseDir string, opts ...Option) (*Store, error) {
	if baseDir == "" {
		return nil, errors.New("runs: baseDir is required")
	}
	s := &Store{
		baseDir: baseDir,
		now:     time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// BaseDir returns the store's root directory.
func (s *Store) BaseDir() string { return s.baseDir }

// Open mints a new run-id, creates its directory, and writes the
// initial manifest. device and owner are required; note is optional.
func (s *Store) Open(device, owner, note string) (*Run, error) {
	if device == "" {
		return nil, errors.New("runs: device is required")
	}
	if owner == "" {
		return nil, errors.New("runs: owner is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("runs: mkdir base: %w", err)
	}

	now := s.now()
	id, err := s.mintIDLocked(now)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.baseDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("runs: mkdir %s: %w", dir, err)
	}
	r := &Run{
		ID:        id,
		Device:    device,
		Owner:     owner,
		Note:      note,
		CreatedAt: now,
	}
	if err := writeManifest(dir, r); err != nil {
		return nil, err
	}
	return r, nil
}

// Close stamps ClosedAt on the run's manifest. Idempotent: closing an
// already-closed run is a no-op.
func (s *Store) Close(runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, r, err := s.readLocked(runID)
	if err != nil {
		return err
	}
	if r.ClosedAt != nil {
		return nil
	}
	t := s.now()
	r.ClosedAt = &t
	return writeManifest(dir, r)
}

// Active returns the most recent open run (ClosedAt == nil) matching
// device and owner, or nil if none. Device and owner match verbatim;
// the caller should normalize via the inventory first if aliasing is
// expected.
func (s *Store) Active(device, owner string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.listLocked()
	if err != nil {
		return nil, err
	}
	// listLocked returns newest first.
	for i := range runs {
		r := &runs[i]
		if r.ClosedAt == nil && r.Device == device && r.Owner == owner {
			return r, nil
		}
	}
	return nil, nil
}

// AddArtefact writes data under the run's directory and appends an
// Artefact record to the manifest. name is the on-disk file name; if
// empty, one is generated from source + timestamp. Returns the
// resulting Artefact record.
func (s *Store) AddArtefact(runID, source, name, mime string, data []byte) (Artefact, error) {
	if source == "" {
		return Artefact{}, errors.New("runs: source is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	dir, r, err := s.readLocked(runID)
	if err != nil {
		return Artefact{}, err
	}
	if r.ClosedAt != nil {
		return Artefact{}, fmt.Errorf("runs: run %s is closed", runID)
	}

	now := s.now()
	if name == "" {
		name = fmt.Sprintf("%s-%s.bin", source, now.UTC().Format("20060102-150405"))
	}
	name = filepath.Base(name) // prevent path traversal
	if name == "manifest.json" {
		return Artefact{}, errors.New("runs: artefact name 'manifest.json' is reserved")
	}

	dst := filepath.Join(dir, name)
	if err := atomicWrite(dst, data, 0o600); err != nil {
		return Artefact{}, fmt.Errorf("runs: write artefact: %w", err)
	}
	a := Artefact{
		Name:      name,
		Source:    source,
		MIMEType:  mime,
		Size:      int64(len(data)),
		CreatedAt: now,
	}
	r.Artefacts = append(r.Artefacts, a)
	if err := writeManifest(dir, r); err != nil {
		// Roll back the artefact file so manifest and filesystem agree.
		_ = os.Remove(dst)
		return Artefact{}, err
	}
	return a, nil
}

// List returns all runs (open and closed), newest first by CreatedAt.
// Non-manifest dirs and unreadable manifests are skipped silently so
// a stray directory doesn't break the whole enumeration.
func (s *Store) List() ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

// Get returns a single run by id. Returns os.ErrNotExist if no such
// run dir exists.
func (s *Store) Get(runID string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, r, err := s.readLocked(runID)
	return r, err
}

// PruneResult describes a prune pass.
type PruneResult struct {
	Removed  []string // run-ids deleted
	Retained int      // number of runs remaining
}

// Prune applies the store's Policy: deletes runs older than MaxAge,
// then deletes oldest runs until remaining size ≤ MaxSize. No-op if
// both bounds are zero. Never prunes open runs.
func (s *Store) Prune() (PruneResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	runs, err := s.listLocked()
	if err != nil {
		return PruneResult{}, err
	}

	var removed []string
	now := s.now()

	// Pass 1: age-based. Newest-first order, so walk backwards.
	if s.policy.MaxAge > 0 {
		cutoff := now.Add(-s.policy.MaxAge)
		kept := runs[:0]
		for _, r := range runs {
			if r.ClosedAt == nil {
				kept = append(kept, r)
				continue
			}
			if r.CreatedAt.Before(cutoff) {
				if err := os.RemoveAll(filepath.Join(s.baseDir, r.ID)); err == nil {
					removed = append(removed, r.ID)
					continue
				}
			}
			kept = append(kept, r)
		}
		runs = kept
	}

	// Pass 2: size-based. Prune oldest closed runs first. Size means
	// artefact bytes — manifest overhead is measured in kilobytes and
	// isn't what the retention knob is trying to cap.
	if s.policy.MaxSize > 0 {
		sizes := make(map[string]int64, len(runs))
		var total int64
		for _, r := range runs {
			sz := runSize(r)
			sizes[r.ID] = sz
			total += sz
		}
		// Oldest first.
		sort.SliceStable(runs, func(i, j int) bool {
			return runs[i].CreatedAt.Before(runs[j].CreatedAt)
		})
		kept := runs[:0]
		for _, r := range runs {
			if total <= s.policy.MaxSize {
				kept = append(kept, r)
				continue
			}
			if r.ClosedAt == nil {
				kept = append(kept, r)
				continue
			}
			dir := filepath.Join(s.baseDir, r.ID)
			if err := os.RemoveAll(dir); err != nil {
				kept = append(kept, r)
				continue
			}
			removed = append(removed, r.ID)
			total -= sizes[r.ID]
		}
		runs = kept
	}

	return PruneResult{Removed: removed, Retained: len(runs)}, nil
}

// --- internals ------------------------------------------------------

// mintIDLocked generates a fresh run-id derived from the clock plus
// a short random suffix to break same-second ties.
func (s *Store) mintIDLocked(now time.Time) (string, error) {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("runs: mint id: %w", err)
	}
	return fmt.Sprintf("%s-%s",
		now.UTC().Format("20060102-150405"),
		hex.EncodeToString(buf[:])), nil
}

// readLocked loads one run's manifest by id.
func (s *Store) readLocked(runID string) (string, *Run, error) {
	if runID == "" {
		return "", nil, errors.New("runs: run_id is required")
	}
	// Guard against path traversal. Run ids never contain separators.
	if strings.ContainsAny(runID, `/\`) || runID == ".." || runID == "." {
		return "", nil, fmt.Errorf("runs: invalid run_id %q", runID)
	}
	dir := filepath.Join(s.baseDir, runID)
	r, err := readManifest(dir)
	if err != nil {
		return "", nil, err
	}
	return dir, r, nil
}

// listLocked enumerates every run dir under baseDir.
func (s *Store) listLocked() ([]Run, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("runs: read base: %w", err)
	}
	out := make([]Run, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.baseDir, e.Name())
		r, err := readManifest(dir)
		if err != nil {
			// Silently skip — unreadable/missing manifest means this
			// isn't a valid run dir.
			continue
		}
		out = append(out, *r)
	}
	// Newest first.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// runSize sums the artefact bytes recorded in a run's manifest.
func runSize(r Run) int64 {
	var sum int64
	for _, a := range r.Artefacts {
		sum += a.Size
	}
	return sum
}

// --- manifest I/O ---------------------------------------------------

const manifestName = "manifest.json"

func readManifest(dir string) (*Run, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("runs: parse manifest %s: %w", dir, err)
	}
	return &r, nil
}

func writeManifest(dir string, r *Run) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, manifestName), data, 0o600)
}

// atomicWrite stages via a sibling .tmp file then renames, so a
// concurrent reader never sees a half-written manifest.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
