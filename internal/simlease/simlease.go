// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package simlease gives callers expiring leases on iOS simulators so
// that spyder, not the caller, decides when a simulator is booted and
// shut down (🎯T188).
//
// Acquire records what it did to the simulator — created it, booted
// it, or found it already running — and Release undoes exactly that.
// A lease expires unless renewed (explicitly or by device activity);
// the reaper releases expired leases the same way. Lease records live
// in a JSON file so the reaper picks up where it left off after a
// daemon restart. Booted simulators with no lease are reported but
// never touched: the owner boots simulators by hand too.
package simlease

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Action records what Acquire did to the simulator, and therefore what
// Release must undo.
type Action string

const (
	// ActionCreated: spyder created the simulator. Release deletes it.
	ActionCreated Action = "created"
	// ActionBooted: the simulator existed shut down; spyder booted it.
	// Release shuts it down.
	ActionBooted Action = "booted"
	// ActionAlreadyRunning: the simulator was booted before the lease.
	// Release leaves it alone.
	ActionAlreadyRunning Action = "already_running"
)

const (
	// DefaultTTL applies when Acquire is given no TTL.
	DefaultTTL = 30 * time.Minute
	// MaxTTL caps a requested TTL.
	MaxTTL = 12 * time.Hour
	// MinTTL floors a requested TTL so a lease cannot lapse mid-boot.
	MinTTL = time.Minute
	// ReapInterval is how often Run checks for expired leases.
	ReapInterval = 30 * time.Second
	// NamePrefix names simulators that Acquire creates.
	NamePrefix = "spyder-lease-"
	// touchPersistEvery limits how often activity-driven renewals are
	// written to disk; in memory the expiry always moves.
	touchPersistEvery = 30 * time.Second
	// poolNamePrefix marks the template pool's simulators (internal/pool),
	// which the pool manages and leases must not take.
	poolNamePrefix = "spyder-pool-"
)

// Lease is one caller's hold on one simulator.
type Lease struct {
	ID         string    `json:"id"`
	UDID       string    `json:"udid"`
	Name       string    `json:"name"`
	DeviceType string    `json:"device_type"`
	Runtime    string    `json:"runtime"`
	Owner      string    `json:"owner"`
	Project    string    `json:"project,omitempty"`
	Action     Action    `json:"action"`
	TTLSeconds int64     `json:"ttl_s"`
	AcquiredAt time.Time `json:"acquired_at"`
	RenewedAt  time.Time `json:"renewed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// Pending is true until a create or boot completes.
	Pending bool `json:"pending,omitempty"`
	// OperationPID is the local process running an acquire or undo. The
	// book is shared on this host; a peer must wait for that process to
	// finish or die. Legacy pending records have no PID and can be reaped.
	OperationPID int `json:"operation_pid,omitempty"`
	// LastError is the most recent failed release attempt; the reaper
	// retries until the release succeeds.
	LastError string `json:"last_error,omitempty"`
}

func (l Lease) ttl() time.Duration { return time.Duration(l.TTLSeconds) * time.Second }

// Unleased is a booted simulator spyder holds no lease for.
type Unleased struct {
	UDID       string `json:"udid"`
	Name       string `json:"name"`
	DeviceType string `json:"device_type"`
	Runtime    string `json:"runtime"`
	// UptimeSeconds is how long it has been booted; 0 when unknown.
	UptimeSeconds int64 `json:"uptime_s"`
	// Pool is true for the template pool's spyder-pool-* simulators,
	// which the pool's own linger timer manages.
	Pool bool `json:"pool,omitempty"`
}

// Status is the full lease picture.
type Status struct {
	Leases   []Lease    `json:"leases"`
	Unleased []Unleased `json:"unleased_booted"`
	Errors   []string   `json:"errors,omitempty"`
}

// Released describes what a release did.
type Released struct {
	Lease Lease `json:"lease"`
	// Undo is what release did to the simulator: "deleted",
	// "shut_down", "left_running", or "already_gone".
	Undo string `json:"undo"`
	// Reason is "released" or "expired".
	Reason string `json:"reason"`
}

// Args configures a Manager.
type Args struct {
	Simctl Simctl
	// Path is the lease file. Empty keeps leases in memory only (tests).
	Path string
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// OnRelease, when set, is called after every successful release
	// (explicit or expired). It must not call back into the Manager.
	OnRelease func(Released)
	// ProvenancePath is the provenance ledger (🎯T189): who made or last
	// leased each device, claims, and spyder's last use. Empty keeps it
	// in memory only.
	ProvenancePath string
	// AuditPath, when set, receives one JSON line per device or runtime
	// that a prune deletes.
	AuditPath string
	// Android lists and deletes AVDs and system images. Nil leaves them
	// out of the inventory.
	Android AndroidHost
}

// Manager owns the lease book.
type Manager struct {
	sim       Simctl
	path      string
	now       func() time.Time
	onRelease func(Released)

	provPath  string
	auditPath string
	android   AndroidHost

	mu              sync.Mutex
	leases          map[string]*Lease      // by lease ID
	inFlight        map[string]bool        // lease IDs an Acquire in this process is still working on
	removed         map[string]bool        // lease IDs this process deleted; a later reload must not bring them back
	bookLock        *os.File               // exclusive flock on path+".lock" while a critical section holds it
	prov            map[string]*Provenance // by provKey(device id)
	pruning         map[string]bool        // provKey'd ids a prune is deleting
	lastPersist     time.Time
	lastProvPersist time.Time
}

// New loads the lease file (if any) and returns a Manager. It does not
// reap; call Reap or Run for that.
func New(args *Args) (*Manager, error) {
	m := &Manager{
		sim:       args.Simctl,
		path:      args.Path,
		now:       args.Now,
		onRelease: args.OnRelease,
		provPath:  args.ProvenancePath,
		auditPath: args.AuditPath,
		android:   args.Android,
		leases:    map[string]*Lease{},
		inFlight:  map[string]bool{},
		removed:   map[string]bool{},
		prov:      map[string]*Provenance{},
		pruning:   map[string]bool{},
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.path != "" {
		list, err := readLeaseFile(m.path)
		if err != nil {
			return nil, err
		}
		for i := range list {
			l := list[i]
			if l.ID == "" {
				continue
			}
			m.leases[l.ID] = &l
		}
	}
	if err := m.loadProvenance(); err != nil {
		return nil, err
	}
	return m, nil
}

// persistLocked merges this manager's leases into the on-disk book and
// writes that. Callers hold m.mu. Two managers share one file (DH-16):
// the last writer must not drop a lease it does not have in memory, and
// a lease this process removed must not come back from an older copy.
func (m *Manager) persistLocked() error {
	m.lastPersist = m.now()
	if m.path == "" {
		return nil
	}
	owned := m.bookLock == nil
	if owned {
		if err := m.lockBook(); err != nil {
			return err
		}
		defer m.unlockBook()
	}
	return m.writeMergedLocked()
}

// beginBook takes the file lock and loads the book. Callers hold m.mu.
// A peer's lease written after New is visible before this manager
// chooses a simulator. unlockBook releases the lock; it does not write.
func (m *Manager) beginBook() error {
	if err := m.lockBook(); err != nil {
		return err
	}
	if err := m.adoptDiskLocked(); err != nil {
		m.unlockBook()
		return err
	}
	return nil
}

func (m *Manager) lockBook() error {
	if m.path == "" || m.bookLock != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(m.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return err
	}
	m.bookLock = f
	return nil
}

func (m *Manager) unlockBook() {
	if m.bookLock == nil {
		return
	}
	if err := m.bookLock.Close(); err != nil {
		slog.Error("simlease: unlock", "path", m.path, "error", err)
	}
	m.bookLock = nil
}

func (m *Manager) noteRemoved(id string) {
	m.removed[id] = true
}

// adoptDiskLocked copies the file into m.leases. An in-flight lease keeps
// the pointer Acquire is filling in. A removed id stays gone. Callers
// hold m.mu and the file lock.
func (m *Manager) adoptDiskLocked() error {
	if m.path == "" {
		return nil
	}
	list, err := readLeaseFile(m.path)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range list {
		src := list[i]
		if src.ID == "" || m.removed[src.ID] {
			continue
		}
		seen[src.ID] = true
		if cur, ok := m.leases[src.ID]; ok {
			if m.inFlight[src.ID] {
				continue
			}
			*cur = src
			continue
		}
		l := src
		m.leases[src.ID] = &l
	}
	for id := range m.leases {
		if seen[id] || m.inFlight[id] {
			continue
		}
		delete(m.leases, id)
	}
	return nil
}

func readLeaseFile(path string) ([]Lease, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("simlease: read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var list []Lease
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("simlease: parse %s: %w", path, err)
	}
	return list, nil
}

// writeMergedLocked re-reads the file, drops tombstoned ids, overlays
// this manager's leases, and renames a private temp file over the book.
// Callers hold m.mu and the file lock.
func (m *Manager) writeMergedLocked() error {
	disk, err := readLeaseFile(m.path)
	if err != nil {
		return err
	}
	merged := make(map[string]Lease, len(disk)+len(m.leases))
	for i := range disk {
		l := disk[i]
		if l.ID == "" || m.removed[l.ID] {
			continue
		}
		merged[l.ID] = l
	}
	for id, l := range m.leases {
		if m.removed[id] {
			delete(merged, id)
			continue
		}
		merged[id] = *l
	}
	list := make([]Lease, 0, len(merged))
	for _, l := range merged {
		list = append(list, l)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].AcquiredAt.Before(list[j].AcquiredAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sim-leases-*.json")
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
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
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
	if err := os.Rename(tmpName, m.path); err != nil {
		return err
	}
	keep = true
	if dirf, err := os.Open(dir); err == nil {
		if err := dirf.Sync(); err != nil {
			slog.Error("simlease: directory sync", "path", dir, "error", err)
		}
		_ = dirf.Close()
	}
	for id, src := range merged {
		if cur, ok := m.leases[id]; ok {
			if !m.inFlight[id] {
				*cur = src
			}
			continue
		}
		l := src
		m.leases[id] = &l
	}
	for id := range m.leases {
		if _, ok := merged[id]; ok || m.inFlight[id] {
			continue
		}
		delete(m.leases, id)
	}
	return nil
}

func (m *Manager) sortedLocked() []Lease {
	list := make([]Lease, 0, len(m.leases))
	for _, l := range m.leases {
		list = append(list, *l)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].AcquiredAt.Before(list[j].AcquiredAt) })
	return list
}

func (m *Manager) mustPersistLocked() {
	if err := m.persistLocked(); err != nil {
		slog.Error("simlease: persist failed", "path", m.path, "error", err)
	}
}

// AcquireArgs selects a simulator.
type AcquireArgs struct {
	// DeviceType is a simctl device-type identifier
	// ("com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro") or its
	// name ("iPhone 16 Pro"). Required.
	DeviceType string
	// Runtime is a runtime identifier, a name ("iOS 18.6"), or a bare
	// version ("18.6"). Empty selects the newest available iOS runtime.
	Runtime string
	// Owner identifies the caller. Required.
	Owner string
	// Project optionally names the repository or project the simulator is
	// for, so disk use can be blamed on it (🎯T189).
	Project string
	// Name optionally pins the simulator's name: only a simulator with
	// this name is reused, and a created one gets it. Empty reuses any
	// matching simulator and names a created one spyder-lease-<id>.
	Name string
	// TTL is the lease length; zero means DefaultTTL.
	TTL time.Duration
}

func clampTTL(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultTTL
	case d < MinTTL:
		return MinTTL
	case d > MaxTTL:
		return MaxTTL
	}
	return d
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Acquire leases a simulator matching args. Reuse prefers a shut-down
// simulator (booted for the lease), then a booted one nobody leases
// (left running at release), and otherwise creates one. When the same
// owner already leases a matching simulator, that lease is renewed and
// returned.
func (m *Manager) Acquire(ctx context.Context, args AcquireArgs) (Lease, error) {
	if args.Owner == "" {
		return Lease{}, errors.New("sim_acquire: owner is required")
	}
	if args.DeviceType == "" {
		return Lease{}, errors.New("sim_acquire: device_type is required")
	}
	deviceType, err := m.resolveDeviceType(ctx, args.DeviceType)
	if err != nil {
		return Lease{}, err
	}
	runtime, err := m.resolveRuntime(ctx, args.Runtime)
	if err != nil {
		return Lease{}, err
	}
	ttl := clampTTL(args.TTL)

	m.mu.Lock()
	if err := m.beginBook(); err != nil {
		m.mu.Unlock()
		return Lease{}, err
	}
	now := m.now()
	for _, l := range m.leases {
		if l.Owner == args.Owner && l.DeviceType == deviceType && l.Runtime == runtime &&
			(args.Name == "" || l.Name == args.Name) && !l.Pending && l.OperationPID == 0 && l.LastError == "" {
			if args.Project != "" {
				l.Project = args.Project
			}
			l.TTLSeconds = int64(ttl / time.Second)
			l.RenewedAt = now
			l.ExpiresAt = now.Add(ttl)
			m.mustPersistLocked()
			out := *l
			m.unlockBook()
			m.mu.Unlock()
			return out, nil
		}
	}
	m.unlockBook()
	m.mu.Unlock()

	sims, err := m.sim.List(ctx)
	if err != nil {
		return Lease{}, fmt.Errorf("sim_acquire: list simulators: %w", err)
	}

	m.mu.Lock()
	if err := m.beginBook(); err != nil {
		m.mu.Unlock()
		return Lease{}, err
	}
	leased := map[string]bool{}
	// A created lease is recorded before Create returns, so it has no
	// UDID yet. The simulator can already be listed under its name.
	// Keying "already leased" only by UDID lets a second acquire take
	// it (DH-15).
	pendingNames := map[string]bool{}
	for _, l := range m.leases {
		if l.UDID != "" {
			leased[l.UDID] = true
		}
		if l.Pending && l.Action == ActionCreated && l.Name != "" {
			pendingNames[l.Name] = true
		}
	}
	var shutdown, running *Sim
	for i := range sims {
		s := &sims[i]
		if !s.IsAvailable || s.DeviceType != deviceType || s.Runtime != runtime || leased[s.UDID] ||
			pendingNames[s.Name] ||
			strings.HasPrefix(s.Name, poolNamePrefix) || m.pruning[provKey(s.UDID)] ||
			m.claimedByOtherLocked(s.UDID, args.Owner) {
			continue
		}
		if args.Name != "" && s.Name != args.Name {
			continue
		}
		switch {
		case s.State == "Shutdown" && shutdown == nil:
			shutdown = s
		case s.Booted() && running == nil:
			running = s
		}
	}
	l := &Lease{
		ID:         newID(),
		DeviceType: deviceType,
		Runtime:    runtime,
		Owner:      args.Owner,
		Project:    args.Project,
		TTLSeconds: int64(ttl / time.Second),
		AcquiredAt: now,
		RenewedAt:  now,
		ExpiresAt:  m.now().Add(ttl),
	}
	switch {
	case shutdown != nil:
		l.UDID, l.Name, l.Action, l.Pending = shutdown.UDID, shutdown.Name, ActionBooted, true
	case running != nil:
		l.UDID, l.Name, l.Action = running.UDID, running.Name, ActionAlreadyRunning
	default:
		l.Name = args.Name
		if l.Name == "" {
			l.Name = createdLeaseName(l.ID)
		}
		l.Action, l.Pending = ActionCreated, true
	}
	// Recorded before any side effect, so a daemon that dies mid-acquire
	// leaves a pending record the next daemon's reaper undoes, and a
	// concurrent Acquire skips the claimed simulator.
	m.leases[l.ID] = l
	if l.Pending {
		m.inFlight[l.ID] = true
		l.OperationPID = os.Getpid()
	}
	m.mustPersistLocked()
	m.unlockBook()
	if l.Action == ActionAlreadyRunning {
		m.recordAcquireLocked(l)
		out := *l
		m.mu.Unlock()
		slog.Info("simlease: acquired running simulator", "lease", out.ID, "udid", out.UDID, "owner", out.Owner)
		return out, nil
	}
	m.mu.Unlock()

	if l.Action == ActionCreated {
		udid, err := m.sim.Create(ctx, l.Name, deviceType, runtime)
		if err != nil {
			// Create returned before a simulator existed. Undoing by the
			// pinned name would shut down and delete someone else's
			// simulator (DH-08). A cancelled call may have created one
			// whose UDID was never recorded; that record stays pending
			// for the reaper, which matches name, type and runtime.
			if ctx.Err() != nil {
				m.abandon(ctx, l.ID)
			} else {
				m.dropLease(l.ID)
			}
			return Lease{}, fmt.Errorf("sim_acquire: create %s (%s, %s): %w", l.Name, deviceType, runtime, err)
		}
		m.mu.Lock()
		if err := m.beginBook(); err != nil {
			slog.Error("simlease: reload before recording udid", "lease", l.ID, "error", err)
		}
		l.UDID = udid
		m.mustPersistLocked()
		m.unlockBook()
		m.mu.Unlock()
	}

	if err := m.sim.Boot(ctx, l.UDID); err != nil {
		m.abandon(ctx, l.ID)
		return Lease{}, fmt.Errorf("sim_acquire: boot %s: %w", l.UDID, err)
	}

	m.mu.Lock()
	// Reload while the lease is still in flight so a peer's newer copy
	// of some other lease replaces ours, and this pointer keeps the UDID
	// Create just recorded.
	if err := m.beginBook(); err != nil {
		slog.Error("simlease: reload before finishing acquire", "lease", l.ID, "error", err)
	}
	delete(m.inFlight, l.ID)
	l.Pending = false
	l.OperationPID = 0
	// The TTL starts once the simulator is usable.
	l.RenewedAt = m.now()
	l.ExpiresAt = l.RenewedAt.Add(ttl)
	m.mustPersistLocked()
	m.unlockBook()
	m.recordAcquireLocked(l)
	out := *l
	m.mu.Unlock()
	slog.Info("simlease: acquired", "lease", out.ID, "udid", out.UDID, "action", out.Action, "owner", out.Owner, "ttl", ttl)
	return out, nil
}

// dropLease forgets a lease that created nothing, so a failed Create
// cannot undo a simulator it does not own (DH-08).
func (m *Manager) dropLease(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.beginBook(); err != nil {
		slog.Error("simlease: reload before drop", "lease", id, "error", err)
	} else {
		defer m.unlockBook()
	}
	delete(m.inFlight, id)
	delete(m.leases, id)
	m.noteRemoved(id)
	m.mustPersistLocked()
}

// abandon undoes a failed acquire so it leaves nothing behind. If the
// undo fails, or the caller's context is done, the record stays
// pending for the reaper.
func (m *Manager) abandon(ctx context.Context, id string) {
	m.mu.Lock()
	if err := m.beginBook(); err != nil {
		slog.Error("simlease: reload before abandoning acquire", "lease", id, "error", err)
	}
	delete(m.inFlight, id)
	if l := m.leases[id]; l != nil {
		l.OperationPID = 0
		m.mustPersistLocked()
	}
	m.unlockBook()
	m.mu.Unlock()
	if ctx.Err() != nil {
		// The caller's deadline has passed; the record is pending and no
		// longer in flight, so the next Reap undoes it.
		return
	}
	if _, err := m.release(ctx, id, "acquire_failed"); err != nil {
		slog.Warn("simlease: undo after failed acquire failed; the reaper will retry", "lease", id, "error", err)
	}
}

func (m *Manager) resolveDeviceType(ctx context.Context, want string) (string, error) {
	if strings.HasPrefix(want, "com.apple.CoreSimulator.SimDeviceType.") {
		return want, nil
	}
	types, err := m.sim.DeviceTypes(ctx)
	if err != nil {
		return "", fmt.Errorf("sim_acquire: list device types: %w", err)
	}
	for _, t := range types {
		if strings.EqualFold(t.Name, want) {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("sim_acquire: unknown device_type %q (pass a simctl identifier or name from `xcrun simctl list devicetypes`)", want)
}

func (m *Manager) resolveRuntime(ctx context.Context, want string) (string, error) {
	if strings.HasPrefix(want, "com.apple.CoreSimulator.SimRuntime.") {
		return want, nil
	}
	rts, err := m.sim.Runtimes(ctx)
	if err != nil {
		return "", fmt.Errorf("sim_acquire: list runtimes: %w", err)
	}
	var best *Runtime
	for i := range rts {
		r := &rts[i]
		if !r.IsAvailable {
			continue
		}
		if want == "" {
			if r.Platform == "iOS" && (best == nil || versionLess(best.Version, r.Version)) {
				best = r
			}
			continue
		}
		if strings.EqualFold(r.Name, want) || r.Version == want ||
			(r.Platform == "iOS" && strings.EqualFold("iOS "+r.Version, want)) {
			return r.ID, nil
		}
	}
	if best != nil {
		return best.ID, nil
	}
	if want == "" {
		return "", errors.New("sim_acquire: no available iOS runtime")
	}
	return "", fmt.Errorf("sim_acquire: unknown runtime %q (pass a simctl identifier, a name such as \"iOS 18.6\", or a version)", want)
}

// versionLess compares dotted numeric versions ("18.10" > "18.6").
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			fmt.Sscan(as[i], &x)
		}
		if i < len(bs) {
			fmt.Sscan(bs[i], &y)
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// find returns the lease named by ref, which may be a lease ID or a
// UDID. Callers hold m.mu.
func (m *Manager) findLocked(ref string) *Lease {
	if l, ok := m.leases[ref]; ok {
		return l
	}
	for _, l := range m.leases {
		if strings.EqualFold(l.UDID, ref) {
			return l
		}
	}
	return nil
}

// ErrNotFound is returned for an unknown lease reference.
var ErrNotFound = errors.New("no such simulator lease")

func (m *Manager) checkOwner(l *Lease, owner string) error {
	if owner != l.Owner {
		return fmt.Errorf("simulator lease %s (%s) is held by %q, not %q", l.ID, l.UDID, l.Owner, owner)
	}
	return nil
}

// Release ends a lease (by lease ID or UDID) held by owner and undoes
// what its acquire did.
func (m *Manager) Release(ctx context.Context, ref, owner string) (Released, error) {
	m.mu.Lock()
	if err := m.beginBook(); err != nil {
		m.mu.Unlock()
		return Released{}, err
	}
	l := m.findLocked(ref)
	m.unlockBook()
	if l == nil {
		m.mu.Unlock()
		return Released{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	if err := m.checkOwner(l, owner); err != nil {
		m.mu.Unlock()
		return Released{}, err
	}
	if l.Pending {
		m.mu.Unlock()
		return Released{}, fmt.Errorf("simulator lease %s is still being acquired", l.ID)
	}
	id := l.ID
	m.mu.Unlock()
	return m.release(ctx, id, "released")
}

// release undoes lease id's action and drops the record. On failure the
// record stays (with LastError) for the reaper to retry.
var errLeaseBusy = errors.New("simulator lease is busy or no longer expired")

func (m *Manager) release(ctx context.Context, id, reason string) (Released, error) {
	m.mu.Lock()
	if err := m.beginBook(); err != nil {
		m.mu.Unlock()
		return Released{}, err
	}
	l, ok := m.leases[id]
	if !ok {
		m.unlockBook()
		m.mu.Unlock()
		return Released{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if m.inFlight[id] || l.operationActive() || (reason == "expired" && !m.reapable(l)) {
		m.unlockBook()
		m.mu.Unlock()
		return Released{}, errLeaseBusy
	}
	snap := *l
	// Claim the undo before dropping the lock. The record remains in
	// the book, so it still protects its simulator during the undo.
	l.OperationPID = os.Getpid()
	if err := m.persistLocked(); err != nil {
		l.OperationPID = snap.OperationPID
		m.unlockBook()
		m.mu.Unlock()
		return Released{}, err
	}
	m.unlockBook()
	m.mu.Unlock()

	undo, err := m.undo(ctx, snap)

	m.mu.Lock()
	defer m.mu.Unlock()
	if bookErr := m.beginBook(); bookErr != nil {
		slog.Error("simlease: reload before release", "lease", id, "error", bookErr)
	} else {
		defer m.unlockBook()
	}
	if err != nil {
		if l, ok := m.leases[id]; ok {
			l.LastError = err.Error()
			l.OperationPID = 0
			m.mustPersistLocked()
		}
		return Released{}, fmt.Errorf("release simulator lease %s (%s): %w", snap.ID, snap.UDID, err)
	}
	delete(m.leases, id)
	m.noteRemoved(id)
	m.mustPersistLocked()
	m.recordReleaseLocked(snap, undo)
	out := Released{Lease: snap, Undo: undo, Reason: reason}
	slog.Info("simlease: released", "lease", snap.ID, "udid", snap.UDID, "owner", snap.Owner,
		"action", snap.Action, "undo", undo, "reason", reason)
	if m.onRelease != nil {
		m.onRelease(out)
	}
	return out, nil
}

// undo reverses l.Action against the simulator's current state.
func (m *Manager) undo(ctx context.Context, l Lease) (string, error) {
	if l.Action == ActionAlreadyRunning {
		return "left_running", nil
	}
	sims, err := m.sim.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list simulators: %w", err)
	}
	cur, ok := m.matchUndoTarget(l, sims)
	if !ok {
		return "already_gone", nil
	}
	if cur.State != "Shutdown" {
		if err := m.sim.Shutdown(ctx, cur.UDID); err != nil {
			return "", fmt.Errorf("shutdown: %w", err)
		}
	}
	if l.Action == ActionCreated {
		if err := m.sim.Delete(ctx, cur.UDID); err != nil {
			return "", fmt.Errorf("delete: %w", err)
		}
		return "deleted", nil
	}
	return "shut_down", nil
}

// matchUndoTarget finds the simulator undo may touch. A recorded UDID
// matches that device only. Without a recorded UDID, require the lease's
// generated name plus the device type and runtime. A caller-supplied
// name cannot prove which simulator a cancelled create touched.
func (m *Manager) matchUndoTarget(l Lease, sims []Sim) (Sim, bool) {
	held, claimed := m.protectedUDIDs(l.ID)
	if l.UDID != "" {
		if held[provKey(l.UDID)] {
			return Sim{}, false
		}
		for i := range sims {
			if strings.EqualFold(sims[i].UDID, l.UDID) {
				return sims[i], true
			}
		}
		return Sim{}, false
	}
	if l.Action != ActionCreated || l.Name == "" || l.Name != createdLeaseName(l.ID) {
		return Sim{}, false
	}
	var found *Sim
	n := 0
	for i := range sims {
		s := &sims[i]
		if s.Name != l.Name || s.DeviceType != l.DeviceType || s.Runtime != l.Runtime {
			continue
		}
		key := provKey(s.UDID)
		if held[key] || claimed[key] {
			continue
		}
		n++
		found = s
	}
	if n != 1 {
		return Sim{}, false
	}
	return *found, true
}

func createdLeaseName(id string) string {
	const prefixLength = 8
	if len(id) < prefixLength {
		return ""
	}
	return NamePrefix + id[:prefixLength]
}

// protectedUDIDs is the set of device ids undo must not guess at.
// exceptLease is the record being undone; its own UDID is not protected
// from itself. Callers do not hold m.mu.
func (m *Manager) protectedUDIDs(exceptLease string) (held, claimed map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held = map[string]bool{}
	claimed = map[string]bool{}
	for _, other := range m.leases {
		if other.ID == exceptLease || other.UDID == "" {
			continue
		}
		held[provKey(other.UDID)] = true
	}
	for id, p := range m.prov {
		if p != nil && p.Claim != nil {
			claimed[id] = true
		}
	}
	return held, claimed
}

// Renew extends a lease held by owner. ttl zero reuses the lease's TTL.
func (m *Manager) Renew(ref, owner string, ttl time.Duration) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.beginBook(); err != nil {
		return Lease{}, err
	}
	defer m.unlockBook()
	l := m.findLocked(ref)
	if l == nil {
		return Lease{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	if err := m.checkOwner(l, owner); err != nil {
		return Lease{}, err
	}
	if l.operationActive() {
		return Lease{}, errLeaseBusy
	}
	if ttl > 0 {
		l.TTLSeconds = int64(clampTTL(ttl) / time.Second)
	}
	l.RenewedAt = m.now()
	l.ExpiresAt = l.RenewedAt.Add(l.ttl())
	m.mustPersistLocked()
	return *l, nil
}

// Touch renews the lease on udid, if any, because the device was used.
// It is cheap and safe to call on every device operation.
func (m *Manager) Touch(udid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	persist := m.path != "" && now.Sub(m.lastPersist) >= touchPersistEvery
	if persist {
		if err := m.beginBook(); err != nil {
			slog.Error("simlease: reload before touch", "path", m.path, "error", err)
		} else {
			defer m.unlockBook()
		}
	}
	for _, l := range m.leases {
		if !strings.EqualFold(l.UDID, udid) || l.Pending || l.OperationPID != 0 {
			continue
		}
		l.RenewedAt = now
		l.ExpiresAt = l.RenewedAt.Add(l.ttl())
		if persist {
			m.mustPersistLocked()
		}
	}
	// Spyder's last use is kept for any simulator it drives, leased or not.
	m.provLocked(udid).LastUsedAt = m.now()
	if m.now().Sub(m.lastProvPersist) >= touchPersistEvery {
		m.persistProvLocked()
	}
}

// Leased reports whether udid is under a lease.
func (m *Manager) Leased(udid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.leases {
		if strings.EqualFold(l.UDID, udid) {
			return true
		}
	}
	return false
}

// Reap releases every expired lease, and every lease left pending by a
// daemon that died mid-acquire. It returns what it released. Failed
// releases stay on the book and are retried on the next pass.
func (m *Manager) Reap(ctx context.Context) []Released {
	m.mu.Lock()
	if err := m.beginBook(); err != nil {
		m.mu.Unlock()
		slog.Warn("simlease: reload before reap", "error", err)
		return nil
	}
	var due []string
	for id, l := range m.leases {
		if m.reapable(l) {
			due = append(due, id)
		}
	}
	m.unlockBook()
	m.mu.Unlock()

	var out []Released
	for _, id := range due {
		r, err := m.release(ctx, id, "expired")
		if errors.Is(err, errLeaseBusy) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			slog.Warn("simlease: reap failed; will retry", "lease", id, "error", err)
			continue
		}
		out = append(out, r)
	}
	return out
}

// reapable requires the current book and m.mu. An orphaned undo is due
// even if the original lease had time remaining when its owner released it.
func (m *Manager) reapable(l *Lease) bool {
	return !m.inFlight[l.ID] && !l.operationActive() &&
		(l.Pending || l.OperationPID != 0 || !m.now().Before(l.ExpiresAt))
}

func (l *Lease) operationActive() bool {
	if l.OperationPID <= 0 {
		return false
	}
	err := syscall.Kill(l.OperationPID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// reapPassTimeout bounds one reaper pass so a hung simctl cannot stall
// every later pass.
const reapPassTimeout = 2 * time.Minute

// Run reaps once immediately and then every interval until ctx ends.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	pass := func() {
		pctx, cancel := context.WithTimeout(ctx, reapPassTimeout)
		defer cancel()
		m.Reap(pctx)
	}
	pass()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}

// Status lists leases and the booted simulators spyder does not lease.
func (m *Manager) Status(ctx context.Context) Status {
	m.mu.Lock()
	st := Status{Leases: m.sortedLocked(), Unleased: []Unleased{}}
	leased := map[string]bool{}
	for _, l := range m.leases {
		leased[strings.ToUpper(l.UDID)] = true
	}
	m.mu.Unlock()

	sims, err := m.sim.List(ctx)
	if err != nil {
		st.Errors = append(st.Errors, fmt.Sprintf("list simulators: %v", err))
		return st
	}
	for _, s := range sims {
		if !s.Booted() || leased[strings.ToUpper(s.UDID)] {
			continue
		}
		st.Unleased = append(st.Unleased, Unleased{
			UDID:          s.UDID,
			Name:          s.Name,
			DeviceType:    s.DeviceType,
			Runtime:       s.Runtime,
			UptimeSeconds: int64(s.Uptime / time.Second),
			Pool:          strings.HasPrefix(s.Name, poolNamePrefix),
		})
	}
	sort.Slice(st.Unleased, func(i, j int) bool { return st.Unleased[i].UptimeSeconds > st.Unleased[j].UptimeSeconds })
	return st
}
