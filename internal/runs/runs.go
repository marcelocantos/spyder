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
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Policy controls run-dir retention. Zero values disable the
// corresponding bound.
//
// Pruning never deletes a run outright: a selected run is archived
// (see Archiver) and its directory is removed only after the archive
// has been verified. Without an archiver, Prune is a no-op.
type Policy struct {
	MaxAge  time.Duration // archive runs older than MaxAge (based on CreatedAt)
	MaxSize int64         // archive oldest runs until total bytes ≤ MaxSize
	// Quiet is how long a run must have been closed, and its files
	// untouched, before it is eligible. Zero disables the quiet check
	// (tests); the daemon uses DefaultQuiet.
	Quiet time.Duration
}

// DefaultQuiet is the daemon's quiet period before a closed run may
// be archived.
const DefaultQuiet = 2 * time.Hour

// Run is the in-memory form of a run dir's manifest.
type Run struct {
	ID        string     `json:"id"`
	Device    string     `json:"device"`
	Owner     string     `json:"owner"`
	Note      string     `json:"note,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	Artefacts []Artefact `json:"artefacts,omitempty"`
	// Versions is stamped at open (🎯T166). Absent on runs created by
	// older spyder builds; the archiver reports those as unknown.
	Versions *Versions `json:"versions,omitempty"`
	// Result is stamped at close.
	Result *Result `json:"result,omitempty"`
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

// WithArchiver installs the archiver Prune uses. Without one, Prune
// never touches any run.
func WithArchiver(a *Archiver) Option { return func(s *Store) { s.archiver = a } }

// Store is the top-level artefact-store handle. Safe for concurrent
// use within one process; the filesystem protocol tolerates multiple
// processes as long as each picks a distinct run-id.
type Store struct {
	mu       sync.Mutex
	baseDir  string
	now      func() time.Time
	policy   Policy
	archiver *Archiver
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
	return s.OpenWith(OpenParams{Device: device, Owner: owner, Note: note})
}

// OpenWith is Open plus source-version provenance (🎯T166). The spyder
// build stamp and the script's git state are computed here; app and
// device-OS fields come from the caller. Anything not observed is
// recorded as "unknown".
func (s *Store) OpenWith(p OpenParams) (*Run, error) {
	device, owner, note := p.Device, p.Owner, p.Note
	if device == "" {
		return nil, errors.New("runs: device is required")
	}
	if owner == "" {
		return nil, errors.New("runs: owner is required")
	}

	vers := buildVersions(p)

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
		Versions:  &vers,
	}
	if err := writeManifest(dir, r); err != nil {
		return nil, err
	}
	slog.Info("runs: opened",
		"run", id, "device", device, "owner", owner, "dir", dir,
		"spyder_commit", vers.Spyder.Commit, "script", vers.Script.Path,
		"app", vers.App.BundleID, "app_version", vers.App.Version,
		"os_version", vers.DeviceOS.Version)
	return r, nil
}

func buildVersions(p OpenParams) Versions {
	sp := SpyderBuild()
	v := Versions{
		App:      p.App,
		Spyder:   sp,
		Script:   ScriptProvenance(p.ScriptPath, p.ScriptSource, sp),
		DeviceOS: p.DeviceOS,
	}
	v.normalize()
	return v
}

// UpdateProvenance fills in provenance on an existing open run (e.g. a
// same-owner re-reserve that now names the script or app). Only known
// values overwrite; nothing is ever downgraded to unknown.
func (s *Store) UpdateProvenance(runID string, p OpenParams) error {
	vers := buildVersions(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, r, err := s.readLocked(runID)
	if err != nil {
		return err
	}
	if r.ClosedAt != nil {
		return fmt.Errorf("runs: run %s is closed", runID)
	}
	if r.Versions == nil {
		u := UnknownVersions()
		r.Versions = &u
	}
	dst := r.Versions
	if p.ScriptPath != "" {
		dst.Script = vers.Script
	}
	for _, f := range []struct {
		d *string
		s string
	}{
		{&dst.App.Name, vers.App.Name}, {&dst.App.BundleID, vers.App.BundleID},
		{&dst.App.Version, vers.App.Version}, {&dst.App.Build, vers.App.Build},
		{&dst.App.Commit, vers.App.Commit}, {&dst.App.Branch, vers.App.Branch},
		{&dst.App.Dirty, vers.App.Dirty},
		{&dst.DeviceOS.Version, vers.DeviceOS.Version}, {&dst.DeviceOS.Build, vers.DeviceOS.Build},
	} {
		mergeKnown(f.d, f.s)
	}
	return writeManifest(dir, r)
}

// Close stamps ClosedAt on the run's manifest. Idempotent: closing an
// already-closed run is a no-op.
func (s *Store) Close(runID string) error {
	return s.CloseWith(runID, CloseParams{})
}

// CloseWith closes the run and stamps its result (🎯T166). Fields the
// caller did not supply are recorded as "unknown".
func (s *Store) CloseWith(runID string, p CloseParams) error {
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
	r.Result = &Result{
		Passed:             orUnknown(p.Passed),
		Outcome:            orUnknown(p.Outcome),
		FailureScreenshots: p.FailureScreenshots,
	}
	if r.Versions == nil {
		u := UnknownVersions()
		r.Versions = &u
	}
	if err := writeManifest(dir, r); err != nil {
		return err
	}
	slog.Info("runs: closed",
		"run", runID, "device", r.Device, "owner", r.Owner,
		"artefacts", len(r.Artefacts),
		"size_bytes", runSize(*r),
		"passed", r.Result.Passed, "outcome", r.Result.Outcome)
	return nil
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
	Removed  []string       // run-ids archived, verified, then removed
	Archived []IndexRecord  // one record per archive written (incl. failed verification)
	Failed   []string       // run-ids whose archive failed; originals kept
	Skipped  map[string]int // reason → count, for runs selected but not archived
	Retained int            // number of run dirs remaining
}

// Prune applies the store's Policy by archiving, never deleting:
// closed runs older than MaxAge, then oldest closed runs until the
// remaining size ≤ MaxSize, are handed to the Archiver, which removes
// a run's directory only after its archive verifies. Open runs, runs
// closed or modified within Policy.Quiet, runs with open file handles
// and runs that already have an archive are skipped. No-op if both
// bounds are zero or no archiver is installed.
//
// The store lock is held only while listing; archiving (slow) runs
// unlocked so Open/AddArtefact/Close are never blocked. Closed runs
// are immutable (AddArtefact rejects them), and the archiver re-checks
// the directory snapshot before removing anything.
func (s *Store) Prune() (PruneResult, error) {
	s.mu.Lock()
	runs, err := s.listLocked()
	s.mu.Unlock()
	if err != nil {
		return PruneResult{}, err
	}
	res := PruneResult{Skipped: map[string]int{}}
	if s.policy.MaxAge <= 0 && s.policy.MaxSize <= 0 {
		res.Retained = len(runs)
		return res, nil
	}
	if s.archiver == nil {
		slog.Warn("runs: retention bound hit but no archiver configured; nothing pruned")
		res.Retained = len(runs)
		return res, nil
	}
	unlock, lerr := s.archiver.lockPass()
	if lerr != nil {
		slog.Info("runs: archive pass skipped", "reason", lerr)
		res.Retained = len(runs)
		return res, nil
	}
	defer unlock()

	now := s.now()
	archive := func(r Run) bool {
		if why := s.eligible(r, now); why != "" {
			res.Skipped[why]++
			return false
		}
		rec, err := s.archiver.ArchiveRun(filepath.Join(s.baseDir, r.ID))
		if rec != nil {
			res.Archived = append(res.Archived, *rec)
		}
		switch {
		case err != nil && rec == nil:
			res.Skipped[err.Error()]++
			return false
		case err != nil:
			res.Failed = append(res.Failed, r.ID)
			return false
		case rec.OriginalRemoved:
			res.Removed = append(res.Removed, r.ID)
			return true
		}
		return false
	}

	// Pass 1: age-based.
	kept := runs[:0]
	for _, r := range runs {
		if s.policy.MaxAge > 0 && r.CreatedAt.Before(now.Add(-s.policy.MaxAge)) && archive(r) {
			continue
		}
		kept = append(kept, r)
	}
	runs = kept

	// Pass 2: size-based, oldest first. Size means artefact bytes.
	if s.policy.MaxSize > 0 {
		var total int64
		for _, r := range runs {
			total += runSize(r)
		}
		sort.SliceStable(runs, func(i, j int) bool {
			return runs[i].CreatedAt.Before(runs[j].CreatedAt)
		})
		kept := runs[:0]
		for _, r := range runs {
			if total > s.policy.MaxSize && archive(r) {
				total -= runSize(r)
				continue
			}
			kept = append(kept, r)
		}
		runs = kept
	}
	res.Retained = len(runs)
	return res, nil
}

// eligible returns "" when r may be archived, else the skip reason.
func (s *Store) eligible(r Run, now time.Time) string {
	if r.ClosedAt == nil {
		return "open"
	}
	q := s.policy.Quiet
	if q <= 0 {
		return ""
	}
	if now.Sub(*r.ClosedAt) < q {
		return "closed recently"
	}
	if mt, err := latestMtime(filepath.Join(s.baseDir, r.ID)); err != nil || now.Sub(mt) < q {
		return "modified recently"
	}
	return ""
}

func latestMtime(dir string) (time.Time, error) {
	var latest time.Time
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest, err
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
