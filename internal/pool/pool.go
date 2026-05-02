// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pool

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/marcelocantos/spyder/internal/poolstore"
)

// PoolNamePrefix marks sims/AVDs as spyder-pool-owned. The suffix is
// the first 8 chars of the instance UUID; nothing parses it — the
// prefix is purely an ownership predicate ("is this mine?") used at
// adoption and GC time.
const PoolNamePrefix = "spyder-pool-"

// Tier indicates which readiness tier an instance occupies.
type Tier string

const (
	// TierAvailable: created on disk, not booted. Cheap to spin up (~seconds).
	TierAvailable Tier = "available"
	// TierRunning: booted and idle, ready in ~milliseconds.
	TierRunning Tier = "running"
	// TierReserved: currently handed off to a caller.
	TierReserved Tier = "reserved"
)

// Instance is a single sim/emu managed by the pool.
type Instance struct {
	ID            string    `json:"id"`
	Template      string    `json:"template"`
	Platform      string    `json:"platform"`
	Tier          Tier      `json:"tier"`
	DeviceID      string    `json:"device_id"`        // UDID (iOS) or AVD name (Android)
	Serial        string    `json:"serial,omitempty"` // emulator-5554 when booted (Android)
	Holder        string    `json:"holder,omitempty"` // populated when Tier == Reserved
	CreatedAt     time.Time `json:"created_at"`
	AcquiredAt    time.Time `json:"acquired_at,omitempty"`
	LastReleaseAt time.Time `json:"last_release_at,omitempty"`
}

// Executor abstracts the simctl/avdmanager calls so tests can inject a fake.
type Executor interface {
	// SimCreate creates a new iOS simulator and returns its UDID.
	SimCreate(name, deviceTypeID, runtimeID string) (string, error)
	// SimBoot boots a simulator by UDID.
	SimBoot(udid string) error
	// SimShutdown shuts down a simulator by UDID.
	SimShutdown(udid string) error
	// SimDelete deletes a simulator by UDID.
	SimDelete(udid string) error
	// SimList returns all simulator UDIDs and their states.
	SimList() ([]SimInfo, error)

	// AVDClone clones a template AVD into a new AVD and returns its name.
	AVDClone(templateName, newName string) error
	// AVDBoot starts an AVD and returns its emulator serial.
	AVDBoot(name string) (string, error)
	// AVDShutdown stops the emulator with the given serial.
	AVDShutdown(serial string) error
	// AVDDelete deletes an AVD by name.
	AVDDelete(name string) error
	// AVDList returns all AVD names.
	AVDList() ([]AVDInfo, error)
}

// SimInfo is the minimal sim metadata the pool needs.
type SimInfo struct {
	UDID  string
	Name  string
	State string // "Booted", "Shutdown", etc.
}

// AVDInfo is the minimal AVD metadata the pool needs.
type AVDInfo struct {
	Name string
	Path string
}

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a handle to a scheduled callback.
type Timer interface {
	Stop() bool
}

// realClock is the production Clock.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

// TemplateStatus is a point-in-time snapshot of one template's pool state.
type TemplateStatus struct {
	Template  string     `json:"template"`
	Platform  string     `json:"platform"`
	Instances []Instance `json:"instances"`
	// Counts by tier for convenience.
	Available int `json:"available"`
	Running   int `json:"running"`
	Reserved  int `json:"reserved"`
}

// Pool manages the lifecycle of pooled sim/emu instances.
type Pool struct {
	mu        sync.Mutex
	cfg       *Config
	exec      Executor
	clk       Clock
	store     *poolstore.Store     // optional persistent hold ledger
	instances map[string]*Instance // keyed by instance.ID
	// lingerTimers tracks active linger timers by instance ID.
	lingerTimers map[string]Timer
}

// Option configures a Pool at construction.
type Option func(*Pool)

// WithStore wires a persistent hold ledger so reservations survive
// daemon restarts. If nil (the default), holds are in-memory only.
func WithStore(s *poolstore.Store) Option {
	return func(p *Pool) { p.store = s }
}

// New creates a Pool with the given config and the real executors.
// Call Adopt(ctx) then Reconcile(ctx) once the daemon is ready to
// bring the pool to desired state.
func New(cfg *Config, exec Executor, opts ...Option) *Pool {
	return newWithClock(cfg, exec, realClock{}, opts...)
}

// newWithClock creates a Pool with an injectable clock (for tests).
func newWithClock(cfg *Config, exec Executor, clk Clock, opts ...Option) *Pool {
	p := &Pool{
		cfg:          cfg,
		exec:         exec,
		clk:          clk,
		instances:    map[string]*Instance{},
		lingerTimers: map[string]Timer{},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Adopt rebuilds the in-memory inventory from live simctl/avdmanager
// state plus the persisted hold ledger. Must be called before
// Reconcile on daemon startup; safe to call exactly once. Live sims
// or AVDs whose name starts with PoolNamePrefix are claimed; anything
// else is left alone.
//
// Adoption rules:
//
//   - Live + in ledger → TierReserved, holder restored from ledger.
//   - Live + booted, not in ledger → TierRunning (linger timer armed).
//   - Live + shutdown, not in ledger → TierAvailable.
//   - In ledger but not live → ledger row deleted (user removed the sim).
//
// Sims found in live state but whose template no longer exists in
// the config are left alone (they'd be GC candidates if the user runs
// pool_gc).
func (p *Pool) Adopt(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	holdsByDevice := map[string]poolstore.Hold{}
	if p.store != nil {
		holds, err := p.store.List()
		if err != nil {
			return fmt.Errorf("pool adopt: list holds: %w", err)
		}
		for _, h := range holds {
			holdsByDevice[h.DeviceID] = h
		}
	}

	// Build a set of live spyder-pool device IDs so we can prune
	// stale ledger rows for sims the user deleted.
	liveDevices := map[string]bool{}

	// iOS sims.
	sims, err := p.exec.SimList()
	if err != nil {
		slog.Warn("pool adopt: SimList failed; skipping iOS adoption", "error", err)
	} else {
		for _, s := range sims {
			if !strings.HasPrefix(s.Name, PoolNamePrefix) {
				continue
			}
			liveDevices[s.UDID] = true
			tmpl := p.findTemplateForDevice("ios", holdsByDevice)
			if tmpl == "" {
				slog.Info("pool adopt: orphaned ios sim (no matching template) — leaving on disk; run pool_gc to delete",
					"udid", s.UDID, "name", s.Name)
				continue
			}
			p.adoptInstance(adoptInput{
				platform:  "ios",
				deviceID:  s.UDID,
				template:  tmpl,
				booted:    s.State == "Booted",
				holdsByID: holdsByDevice,
			})
		}
	}

	// Android AVDs.
	avds, err := p.exec.AVDList()
	if err != nil {
		slog.Warn("pool adopt: AVDList failed; skipping Android adoption", "error", err)
	} else {
		for _, a := range avds {
			if !strings.HasPrefix(a.Name, PoolNamePrefix) {
				continue
			}
			liveDevices[a.Name] = true
			tmpl := p.findTemplateForDevice("android", holdsByDevice)
			if tmpl == "" {
				slog.Info("pool adopt: orphaned android AVD (no matching template) — leaving on disk; run pool_gc to delete",
					"name", a.Name)
				continue
			}
			// AVDList doesn't surface boot state — we conservatively
			// treat unheld AVDs as available; if they're actually
			// booted, the next AVDBoot returns the existing serial.
			p.adoptInstance(adoptInput{
				platform:  "android",
				deviceID:  a.Name,
				template:  tmpl,
				booted:    false,
				holdsByID: holdsByDevice,
			})
		}
	}

	// Prune ledger rows whose device is gone.
	if p.store != nil {
		for devID, h := range holdsByDevice {
			if !liveDevices[devID] {
				slog.Warn("pool adopt: holder's device vanished — releasing hold",
					"instance_id", h.InstanceID, "device_id", devID,
					"holder", h.Holder, "template", h.Template)
				if err := p.store.DeleteByDevice(devID); err != nil {
					slog.Warn("pool adopt: failed to delete stale hold",
						"device_id", devID, "error", err)
				}
			}
		}
	}
	return nil
}

// adoptInput bundles arguments for adoptInstance to keep the loop
// bodies tidy. Caller must hold p.mu.
type adoptInput struct {
	platform  string
	deviceID  string
	template  string
	booted    bool
	holdsByID map[string]poolstore.Hold
}

func (p *Pool) adoptInstance(in adoptInput) {
	tmpl := p.findTemplate(in.template)
	if tmpl == nil {
		return
	}
	hold, held := in.holdsByID[in.deviceID]

	tier := TierAvailable
	if held {
		tier = TierReserved
	} else if in.booted {
		tier = TierRunning
	}

	id := uuid.New().String()
	if held {
		id = hold.InstanceID
	}
	inst := &Instance{
		ID:        id,
		Template:  in.template,
		Platform:  in.platform,
		Tier:      tier,
		DeviceID:  in.deviceID,
		CreatedAt: p.clk.Now(),
	}
	if held {
		inst.Holder = hold.Holder
		inst.AcquiredAt = hold.AcquiredAt
	}
	p.instances[inst.ID] = inst

	switch tier {
	case TierReserved:
		slog.Info("pool adopt: restored reserved instance",
			"id", inst.ID, "device_id", inst.DeviceID,
			"holder", inst.Holder, "template", inst.Template)
	case TierRunning:
		// Arm linger so it doesn't sit booted forever after adoption.
		linger := tmpl.LingerDuration()
		instanceID := inst.ID
		inst.LastReleaseAt = p.clk.Now()
		p.lingerTimers[inst.ID] = p.clk.AfterFunc(linger, func() {
			p.onLingerExpired(instanceID)
		})
		slog.Info("pool adopt: restored running instance (linger armed)",
			"id", inst.ID, "device_id", inst.DeviceID,
			"template", inst.Template, "linger", linger)
	case TierAvailable:
		slog.Info("pool adopt: restored available instance",
			"id", inst.ID, "device_id", inst.DeviceID, "template", inst.Template)
	}
}

// findTemplateForDevice returns the template name to associate with a
// live device. The device's name (spyder-pool-<8hex>) doesn't encode
// the template, so we recover it via two routes: (a) if the device is
// in the ledger, the ledger's template wins; (b) otherwise, if
// exactly one configured template matches the platform, pick it. With
// zero or multiple platform matches and no ledger entry, return ""
// and the caller leaves the device alone (it'll appear as a GC
// candidate later).
func (p *Pool) findTemplateForDevice(platform string, holdsByDevice map[string]poolstore.Hold) string {
	var matches []string
	for _, t := range p.cfg.Templates {
		if t.Platform == platform {
			matches = append(matches, t.Name)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	// Otherwise, only the ledger can disambiguate.
	for _, h := range holdsByDevice {
		if h.Platform == platform {
			return h.Template
		}
	}
	return ""
}


// Acquire reserves an instance for the named template on behalf of
// holder. It prefers a running instance (near-instant), then boots an
// available one, then mints+boots a new one. The holder identity is
// persisted in the ledger so the reservation survives daemon
// restarts.
func (p *Pool) Acquire(templateName, holder string) (*Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if holder == "" {
		return nil, fmt.Errorf("pool: holder is required")
	}

	tmpl := p.findTemplate(templateName)
	if tmpl == nil {
		return nil, fmt.Errorf("pool: unknown template %q", templateName)
	}

	// Cancel any pending linger timer for an instance we're about to hand off.
	// Prefer running tier first.
	if inst := p.pickRunning(templateName); inst != nil {
		p.cancelLinger(inst.ID)
		p.markReserved(inst, holder)
		slog.Info("pool: acquired from running tier",
			"id", inst.ID, "template", templateName, "holder", holder)
		return inst, nil
	}

	// Fall back to available tier — boot it.
	if inst := p.pickAvailable(templateName); inst != nil {
		if err := p.bootInstance(inst); err != nil {
			return nil, fmt.Errorf("pool: boot for acquire: %w", err)
		}
		p.markReserved(inst, holder)
		slog.Info("pool: acquired from available tier (booted)",
			"id", inst.ID, "template", templateName, "holder", holder)
		return inst, nil
	}

	// Mint a new one.
	inst, err := p.mintInstance(tmpl)
	if err != nil {
		return nil, fmt.Errorf("pool: mint for acquire: %w", err)
	}
	if err := p.bootInstance(inst); err != nil {
		return nil, fmt.Errorf("pool: boot for acquire: %w", err)
	}
	p.markReserved(inst, holder)
	slog.Info("pool: acquired freshly-minted instance",
		"id", inst.ID, "template", templateName, "holder", holder)
	return inst, nil
}

// markReserved transitions inst to TierReserved, stamps holder/acquired,
// and persists the hold to the ledger if one is configured. Must be
// called with p.mu held.
func (p *Pool) markReserved(inst *Instance, holder string) {
	inst.Tier = TierReserved
	inst.Holder = holder
	inst.AcquiredAt = p.clk.Now()
	if p.store != nil {
		err := p.store.Put(poolstore.Hold{
			InstanceID: inst.ID,
			DeviceID:   inst.DeviceID,
			Template:   inst.Template,
			Platform:   inst.Platform,
			Holder:     holder,
			AcquiredAt: inst.AcquiredAt,
		})
		if err != nil {
			slog.Warn("pool: persist hold failed (in-memory state still consistent)",
				"id", inst.ID, "error", err)
		}
	}
}

// Release returns an instance to the pool and starts the linger timer. The
// caller must not use the instance after Release returns.
func (p *Pool) Release(instanceID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	inst, ok := p.instances[instanceID]
	if !ok {
		return fmt.Errorf("pool: unknown instance %q", instanceID)
	}
	if inst.Tier != TierReserved {
		return fmt.Errorf("pool: instance %q is not reserved (tier=%s)", instanceID, inst.Tier)
	}
	inst.Tier = TierRunning
	inst.Holder = ""
	inst.AcquiredAt = time.Time{}
	inst.LastReleaseAt = p.clk.Now()
	if p.store != nil {
		if err := p.store.Delete(instanceID); err != nil {
			slog.Warn("pool: delete hold failed (in-memory state still consistent)",
				"id", instanceID, "error", err)
		}
	}

	tmpl := p.findTemplate(inst.Template)
	if tmpl == nil {
		// Template removed from config; clean up the instance.
		return p.destroyInstance(inst)
	}

	linger := tmpl.LingerDuration()
	timer := p.clk.AfterFunc(linger, func() {
		p.onLingerExpired(instanceID)
	})
	p.lingerTimers[instanceID] = timer
	slog.Info("pool: released to running tier (linger armed)",
		"id", instanceID, "template", inst.Template, "linger", linger)
	return nil
}

// onLingerExpired is called when a linger timer fires. It transitions the
// instance from running to available (shutdown) or deletes it if the
// available tier is at cap.
func (p *Pool) onLingerExpired(instanceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	inst, ok := p.instances[instanceID]
	if !ok {
		return // already deleted
	}
	if inst.Tier != TierRunning {
		return // acquired again before timer fired — nothing to do
	}
	delete(p.lingerTimers, instanceID)

	tmpl := p.findTemplate(inst.Template)
	if tmpl == nil {
		// Template disappeared; delete the instance.
		_ = p.destroyInstance(inst)
		return
	}

	// Cap-driven LRU eviction. If shutting this sim down would push the
	// available-tier population over AvailableMax, delete the oldest
	// available sim for this template first (LRU by LastReleaseAt, then
	// CreatedAt). AvailableMax == 0 means "no cap".
	if tmpl.AvailableMax > 0 {
		_, available, _ := p.countAvailableByTemplate(inst.Template)
		if available+1 > tmpl.AvailableMax {
			if victim := p.pickOldestAvailable(inst.Template); victim != nil {
				slog.Info("pool: linger expired, evicting oldest available (LRU)",
					"evicted_id", victim.ID, "evicted_device", victim.DeviceID,
					"new_id", inst.ID, "template", inst.Template,
					"available_max", tmpl.AvailableMax)
				_ = p.destroyInstance(victim)
			} else {
				// No available sim to evict (everything is reserved or
				// running); deleting this one is the only way to honour
				// the cap.
				slog.Info("pool: linger expired, no available sim to evict — deleting just-released sim",
					"id", instanceID, "template", inst.Template,
					"available_max", tmpl.AvailableMax)
				_ = p.destroyInstance(inst)
				return
			}
		}
	}

	// Shutdown but keep on disk.
	if err := p.shutdownInstance(inst); err != nil {
		slog.Warn("pool: linger expire: shutdown failed",
			"id", instanceID, "error", err)
		// Leave in running tier so it can be reused or retried.
		return
	}
	slog.Info("pool: linger expired, transitioned to available",
		"id", instanceID, "template", inst.Template)
}

// pickOldestAvailable returns the Available instance for a template
// with the oldest LastReleaseAt (or CreatedAt if never released).
// Used for LRU eviction at the cap. Must be called with p.mu held.
func (p *Pool) pickOldestAvailable(name string) *Instance {
	var oldest *Instance
	for _, inst := range p.instances {
		if inst.Template != name || inst.Tier != TierAvailable {
			continue
		}
		if oldest == nil || lastTouch(inst).Before(lastTouch(oldest)) {
			oldest = inst
		}
	}
	return oldest
}

// lastTouch returns the most recent timestamp associated with the
// instance: LastReleaseAt if set, else CreatedAt.
func lastTouch(inst *Instance) time.Time {
	if !inst.LastReleaseAt.IsZero() {
		return inst.LastReleaseAt
	}
	return inst.CreatedAt
}

// ForceShutdown shuts down all running+reserved instances for a template
// and deletes all available instances. Used by pool_drain.
func (p *Pool) ForceShutdown(templateName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	tmpl := p.findTemplate(templateName)
	if tmpl == nil {
		return fmt.Errorf("pool: unknown template %q", templateName)
	}

	var errs []error
	for _, inst := range p.instances {
		if inst.Template != templateName {
			continue
		}
		p.cancelLinger(inst.ID)
		if err := p.destroyInstance(inst); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("pool: drain %q: %d error(s): %v", templateName, len(errs), errs[0])
	}
	return nil
}

// Warm pre-boots n additional instances for the template (capped by
// AvailableMax and AvailableMin after creation).
func (p *Pool) Warm(templateName string, n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	tmpl := p.findTemplate(templateName)
	if tmpl == nil {
		return fmt.Errorf("pool: unknown template %q", templateName)
	}

	var errs []error
	for i := 0; i < n; i++ {
		// Try available first.
		if inst := p.pickAvailable(templateName); inst != nil {
			if err := p.bootInstance(inst); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		// Mint + boot.
		inst, err := p.mintInstance(tmpl)
		if err != nil {
			errs = append(errs, err)
			break
		}
		if err := p.bootInstance(inst); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("pool: warm %q: %d error(s): %v", templateName, len(errs), errs[0])
	}
	return nil
}

// PoolStatus implements the mcp.PoolManager interface. Returns the status
// as any so the mcp package does not need to import pool directly.
func (p *Pool) PoolStatus() any {
	return p.Status()
}

// PoolWarm implements the mcp.PoolManager interface.
func (p *Pool) PoolWarm(template string, n int) error {
	return p.Warm(template, n)
}

// PoolDrain implements the mcp.PoolManager interface.
func (p *Pool) PoolDrain(template string) error {
	return p.ForceShutdown(template)
}

// PoolGC implements the mcp.PoolManager interface.
func (p *Pool) PoolGC() any { return p.GC() }

// GCResult reports what a GC pass deleted (or skipped, for booted
// devices outside the in-memory inventory).
type GCResult struct {
	DeletedSims []string `json:"deleted_sims"`
	DeletedAVDs []string `json:"deleted_avds"`
	// SkippedBooted lists names that match the spyder-pool-* prefix
	// and aren't in the in-memory inventory but are currently Booted —
	// not deleted because they may be in active use by another tool
	// or daemon. Re-run GC after shutting them down.
	SkippedBooted []string `json:"skipped_booted"`
	Errors        []string `json:"errors,omitempty"`
}

// GC deletes orphaned spyder-pool-* sims and AVDs that aren't in the
// in-memory inventory. A sim/AVD is considered orphaned when its name
// has the PoolNamePrefix and its device ID is not tracked by the
// pool. Booted orphans are skipped — the user (or a `pool_gc --force`
// follow-up, not implemented) is expected to verify before deleting
// something potentially in active use.
func (p *Pool) GC() GCResult {
	p.mu.Lock()
	defer p.mu.Unlock()

	tracked := map[string]bool{}
	for _, inst := range p.instances {
		tracked[inst.DeviceID] = true
	}

	res := GCResult{}

	if sims, err := p.exec.SimList(); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("SimList: %v", err))
	} else {
		for _, s := range sims {
			if !strings.HasPrefix(s.Name, PoolNamePrefix) {
				continue
			}
			if tracked[s.UDID] {
				continue
			}
			if s.State == "Booted" {
				res.SkippedBooted = append(res.SkippedBooted, s.Name)
				slog.Info("pool gc: skipping booted orphan (may be in active use)",
					"udid", s.UDID, "name", s.Name)
				continue
			}
			if err := p.exec.SimDelete(s.UDID); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("SimDelete %s: %v", s.UDID, err))
				continue
			}
			res.DeletedSims = append(res.DeletedSims, s.Name)
			slog.Info("pool gc: deleted orphan sim", "udid", s.UDID, "name", s.Name)
		}
	}

	if avds, err := p.exec.AVDList(); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("AVDList: %v", err))
	} else {
		for _, a := range avds {
			if !strings.HasPrefix(a.Name, PoolNamePrefix) {
				continue
			}
			if tracked[a.Name] {
				continue
			}
			if err := p.exec.AVDDelete(a.Name); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("AVDDelete %s: %v", a.Name, err))
				continue
			}
			res.DeletedAVDs = append(res.DeletedAVDs, a.Name)
			slog.Info("pool gc: deleted orphan AVD", "name", a.Name)
		}
	}

	return res
}

// Status returns a snapshot of all templates and their instance counts.
func (p *Pool) Status() []TemplateStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	byTemplate := map[string]*TemplateStatus{}
	for i := range p.cfg.Templates {
		t := &p.cfg.Templates[i]
		byTemplate[t.Name] = &TemplateStatus{
			Template: t.Name,
			Platform: t.Platform,
		}
	}

	for _, inst := range p.instances {
		ts, ok := byTemplate[inst.Template]
		if !ok {
			continue
		}
		cp := *inst
		ts.Instances = append(ts.Instances, cp)
		switch inst.Tier {
		case TierAvailable:
			ts.Available++
		case TierRunning:
			ts.Running++
		case TierReserved:
			ts.Reserved++
		}
	}

	out := make([]TemplateStatus, 0, len(p.cfg.Templates))
	for i := range p.cfg.Templates {
		ts := byTemplate[p.cfg.Templates[i].Name]
		out = append(out, *ts)
	}
	return out
}

// ForSelector returns all TemplateConfigs that match the given selector
// tags (conjunction — all tags must be present). An empty tags list
// matches all templates. This is the hook T23's resolver calls.
func (p *Pool) ForSelector(tags []string) []TemplateConfig {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []TemplateConfig
	for _, t := range p.cfg.Templates {
		if tagsMatch(t.Tags, tags) {
			out = append(out, t)
		}
	}
	return out
}

// TemplateNames returns the names of all configured templates.
func (p *Pool) TemplateNames() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make([]string, len(p.cfg.Templates))
	for i, t := range p.cfg.Templates {
		names[i] = t.Name
	}
	return names
}

// --------------------------------------------------------------------------
// internal helpers — all require p.mu held
// --------------------------------------------------------------------------

func (p *Pool) findTemplate(name string) *TemplateConfig {
	for i := range p.cfg.Templates {
		if p.cfg.Templates[i].Name == name {
			return &p.cfg.Templates[i]
		}
	}
	return nil
}

// countAvailableByTemplate returns (non-available, available, total)
// for a template — useful for cap checks.
func (p *Pool) countAvailableByTemplate(name string) (nonAvail, avail, total int) {
	for _, inst := range p.instances {
		if inst.Template != name {
			continue
		}
		total++
		if inst.Tier == TierAvailable {
			avail++
		} else {
			nonAvail++
		}
	}
	return
}

func (p *Pool) pickRunning(name string) *Instance {
	for _, inst := range p.instances {
		if inst.Template == name && inst.Tier == TierRunning {
			return inst
		}
	}
	return nil
}

func (p *Pool) pickAvailable(name string) *Instance {
	for _, inst := range p.instances {
		if inst.Template == name && inst.Tier == TierAvailable {
			return inst
		}
	}
	return nil
}

// mintInstance creates a new sim/emu from the template. Does NOT boot it.
// Must be called with p.mu held.
func (p *Pool) mintInstance(tmpl *TemplateConfig) (*Instance, error) {
	id := uuid.New().String()
	inst := &Instance{
		ID:        id,
		Template:  tmpl.Name,
		Platform:  tmpl.Platform,
		Tier:      TierAvailable,
		CreatedAt: p.clk.Now(),
	}

	switch tmpl.Platform {
	case "ios":
		cloneName := "spyder-pool-" + id[:8]
		udid, err := p.exec.SimCreate(cloneName, tmpl.DeviceType, tmpl.RuntimeOrSystemImage)
		if err != nil {
			return nil, fmt.Errorf("sim create for template %q: %w", tmpl.Name, err)
		}
		inst.DeviceID = udid

	case "android":
		cloneName := "spyder-pool-" + id[:8]
		if err := p.exec.AVDClone(tmpl.DeviceType, cloneName); err != nil {
			return nil, fmt.Errorf("avd clone for template %q: %w", tmpl.Name, err)
		}
		inst.DeviceID = cloneName

	default:
		return nil, fmt.Errorf("unknown platform %q", tmpl.Platform)
	}

	p.instances[id] = inst
	return inst, nil
}

// bootInstance boots a TierAvailable instance and transitions it to TierRunning.
// Must be called with p.mu held.
func (p *Pool) bootInstance(inst *Instance) error {
	started := time.Now()
	switch inst.Platform {
	case "ios":
		if err := p.exec.SimBoot(inst.DeviceID); err != nil {
			slog.Warn("pool: boot failed",
				"id", inst.ID, "device", inst.DeviceID, "platform", "ios",
				"duration_ms", time.Since(started).Milliseconds(), "error", err)
			return fmt.Errorf("sim boot %s: %w", inst.DeviceID, err)
		}
	case "android":
		serial, err := p.exec.AVDBoot(inst.DeviceID)
		if err != nil {
			slog.Warn("pool: boot failed",
				"id", inst.ID, "device", inst.DeviceID, "platform", "android",
				"duration_ms", time.Since(started).Milliseconds(), "error", err)
			return fmt.Errorf("avd boot %s: %w", inst.DeviceID, err)
		}
		inst.Serial = serial
	}
	inst.Tier = TierRunning
	slog.Info("pool: booted",
		"id", inst.ID, "device", inst.DeviceID, "platform", inst.Platform,
		"duration_ms", time.Since(started).Milliseconds())
	return nil
}

// shutdownInstance shuts down a running instance and transitions it to
// TierAvailable. Must be called with p.mu held.
func (p *Pool) shutdownInstance(inst *Instance) error {
	started := time.Now()
	switch inst.Platform {
	case "ios":
		if err := p.exec.SimShutdown(inst.DeviceID); err != nil {
			slog.Warn("pool: shutdown failed",
				"id", inst.ID, "device", inst.DeviceID, "error", err)
			return fmt.Errorf("sim shutdown %s: %w", inst.DeviceID, err)
		}
	case "android":
		if inst.Serial != "" {
			if err := p.exec.AVDShutdown(inst.Serial); err != nil {
				slog.Warn("pool: shutdown failed",
					"id", inst.ID, "serial", inst.Serial, "error", err)
				return fmt.Errorf("avd shutdown %s: %w", inst.Serial, err)
			}
			inst.Serial = ""
		}
	}
	inst.Tier = TierAvailable
	slog.Info("pool: shut down",
		"id", inst.ID, "device", inst.DeviceID, "platform", inst.Platform,
		"duration_ms", time.Since(started).Milliseconds())
	return nil
}

// destroyInstance shuts down (if running) and deletes an instance from disk
// and from p.instances. Must be called with p.mu held.
func (p *Pool) destroyInstance(inst *Instance) error {
	// Shutdown first if booted.
	if inst.Tier == TierRunning || inst.Tier == TierReserved {
		_ = p.shutdownInstance(inst) // best-effort
	}

	var err error
	switch inst.Platform {
	case "ios":
		err = p.exec.SimDelete(inst.DeviceID)
	case "android":
		err = p.exec.AVDDelete(inst.DeviceID)
	}
	delete(p.instances, inst.ID)
	if p.store != nil {
		_ = p.store.Delete(inst.ID)
	}
	if err != nil {
		slog.Warn("pool: destroyed with deletion error",
			"id", inst.ID, "device", inst.DeviceID, "error", err)
	} else {
		slog.Info("pool: destroyed",
			"id", inst.ID, "device", inst.DeviceID, "platform", inst.Platform)
	}
	return err
}

// cancelLinger stops a pending linger timer for instanceID.
// Must be called with p.mu held.
func (p *Pool) cancelLinger(id string) {
	if t, ok := p.lingerTimers[id]; ok {
		t.Stop()
		delete(p.lingerTimers, id)
	}
}

// tagsMatch returns true when all required tags appear in have.
func tagsMatch(have, required []string) bool {
	if len(required) == 0 {
		return true
	}
	haveSet := make(map[string]bool, len(have))
	for _, t := range have {
		haveSet[t] = true
	}
	for _, t := range required {
		if !haveSet[t] {
			return false
		}
	}
	return true
}
