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
	Action     Action    `json:"action"`
	TTLSeconds int64     `json:"ttl_s"`
	AcquiredAt time.Time `json:"acquired_at"`
	RenewedAt  time.Time `json:"renewed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// Pending is true while Acquire is still creating or booting. A
	// pending lease found on disk at startup is from a daemon that died
	// mid-acquire; the reaper undoes its recorded action.
	Pending bool `json:"pending,omitempty"`
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
}

// Manager owns the lease book.
type Manager struct {
	sim       Simctl
	path      string
	now       func() time.Time
	onRelease func(Released)

	mu          sync.Mutex
	leases      map[string]*Lease // by lease ID
	inFlight    map[string]bool   // lease IDs an Acquire in this process is still working on
	lastPersist time.Time
}

// New loads the lease file (if any) and returns a Manager. It does not
// reap; call Reap or Run for that.
func New(args *Args) (*Manager, error) {
	m := &Manager{
		sim:       args.Simctl,
		path:      args.Path,
		now:       args.Now,
		onRelease: args.OnRelease,
		leases:    map[string]*Lease{},
		inFlight:  map[string]bool{},
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.path != "" {
		data, err := os.ReadFile(m.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, fmt.Errorf("simlease: read %s: %w", m.path, err)
		default:
			var list []Lease
			if err := json.Unmarshal(data, &list); err != nil {
				return nil, fmt.Errorf("simlease: parse %s: %w", m.path, err)
			}
			for i := range list {
				l := list[i]
				m.leases[l.ID] = &l
			}
		}
	}
	return m, nil
}

// persistLocked writes the lease book atomically. Callers hold m.mu.
func (m *Manager) persistLocked() error {
	m.lastPersist = m.now()
	if m.path == "" {
		return nil
	}
	list := m.sortedLocked()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
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
	now := m.now()
	for _, l := range m.leases {
		if l.Owner == args.Owner && l.DeviceType == deviceType && l.Runtime == runtime &&
			(args.Name == "" || l.Name == args.Name) && !l.Pending && l.LastError == "" {
			l.TTLSeconds = int64(ttl / time.Second)
			l.RenewedAt = now
			l.ExpiresAt = now.Add(ttl)
			m.mustPersistLocked()
			out := *l
			m.mu.Unlock()
			return out, nil
		}
	}
	m.mu.Unlock()

	sims, err := m.sim.List(ctx)
	if err != nil {
		return Lease{}, fmt.Errorf("sim_acquire: list simulators: %w", err)
	}

	m.mu.Lock()
	leased := map[string]bool{}
	for _, l := range m.leases {
		leased[l.UDID] = true
	}
	var shutdown, running *Sim
	for i := range sims {
		s := &sims[i]
		if !s.IsAvailable || s.DeviceType != deviceType || s.Runtime != runtime || leased[s.UDID] ||
			strings.HasPrefix(s.Name, poolNamePrefix) {
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
			l.Name = NamePrefix + l.ID[:8]
		}
		l.Action, l.Pending = ActionCreated, true
	}
	// Recorded before any side effect, so a daemon that dies mid-acquire
	// leaves a pending record the next daemon's reaper undoes, and a
	// concurrent Acquire skips the claimed simulator.
	m.leases[l.ID] = l
	if l.Pending {
		m.inFlight[l.ID] = true
	}
	m.mustPersistLocked()
	m.mu.Unlock()

	if l.Action == ActionAlreadyRunning {
		slog.Info("simlease: acquired running simulator", "lease", l.ID, "udid", l.UDID, "owner", l.Owner)
		return *l, nil
	}

	if l.Action == ActionCreated {
		udid, err := m.sim.Create(ctx, l.Name, deviceType, runtime)
		if err != nil {
			m.abandon(ctx, l.ID)
			return Lease{}, fmt.Errorf("sim_acquire: create %s (%s, %s): %w", l.Name, deviceType, runtime, err)
		}
		m.mu.Lock()
		l.UDID = udid
		m.mustPersistLocked()
		m.mu.Unlock()
	}

	if err := m.sim.Boot(ctx, l.UDID); err != nil {
		m.abandon(ctx, l.ID)
		return Lease{}, fmt.Errorf("sim_acquire: boot %s: %w", l.UDID, err)
	}

	m.mu.Lock()
	delete(m.inFlight, l.ID)
	l.Pending = false
	// The TTL starts once the simulator is usable.
	l.RenewedAt = m.now()
	l.ExpiresAt = l.RenewedAt.Add(ttl)
	m.mustPersistLocked()
	out := *l
	m.mu.Unlock()
	slog.Info("simlease: acquired", "lease", out.ID, "udid", out.UDID, "action", out.Action, "owner", out.Owner, "ttl", ttl)
	return out, nil
}

// abandon undoes a failed acquire so it leaves nothing behind. If the
// undo fails, or the caller's context is done, the record stays
// pending for the reaper.
func (m *Manager) abandon(ctx context.Context, id string) {
	m.mu.Lock()
	delete(m.inFlight, id)
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
	l := m.findLocked(ref)
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
func (m *Manager) release(ctx context.Context, id, reason string) (Released, error) {
	m.mu.Lock()
	l, ok := m.leases[id]
	if !ok {
		m.mu.Unlock()
		return Released{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	snap := *l
	m.mu.Unlock()

	undo, err := m.undo(ctx, snap)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		if l, ok := m.leases[id]; ok {
			l.LastError = err.Error()
			m.mustPersistLocked()
		}
		return Released{}, fmt.Errorf("release simulator lease %s (%s): %w", snap.ID, snap.UDID, err)
	}
	delete(m.leases, id)
	m.mustPersistLocked()
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
	var cur *Sim
	for i := range sims {
		s := &sims[i]
		// A created lease whose daemon died before Create returned has no
		// UDID; its spyder-lease-<id> name is unique, so match on that.
		if (l.UDID != "" && strings.EqualFold(s.UDID, l.UDID)) ||
			(l.UDID == "" && l.Action == ActionCreated && s.Name == l.Name) {
			cur = s
			break
		}
	}
	if cur == nil {
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

// Renew extends a lease held by owner. ttl zero reuses the lease's TTL.
func (m *Manager) Renew(ref, owner string, ttl time.Duration) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.findLocked(ref)
	if l == nil {
		return Lease{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	if err := m.checkOwner(l, owner); err != nil {
		return Lease{}, err
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
	for _, l := range m.leases {
		if !strings.EqualFold(l.UDID, udid) || l.Pending {
			continue
		}
		l.RenewedAt = m.now()
		l.ExpiresAt = l.RenewedAt.Add(l.ttl())
		if m.now().Sub(m.lastPersist) >= touchPersistEvery {
			m.mustPersistLocked()
		}
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
	now := m.now()
	var due []string
	for id, l := range m.leases {
		if m.inFlight[id] {
			continue // an Acquire in this process is still creating or booting it
		}
		// A pending record no Acquire here owns is from a daemon that died
		// mid-acquire.
		if l.Pending || !now.Before(l.ExpiresAt) {
			due = append(due, id)
		}
	}
	m.mu.Unlock()

	var out []Released
	for _, id := range due {
		r, err := m.release(ctx, id, "expired")
		if err != nil {
			slog.Warn("simlease: reap failed; will retry", "lease", id, "error", err)
			continue
		}
		out = append(out, r)
	}
	return out
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
