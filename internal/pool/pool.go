// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pool

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

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
	CreatedAt     time.Time `json:"created_at"`
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
	instances map[string]*Instance // keyed by instance.ID
	// lingerTimers tracks active linger timers by instance ID.
	lingerTimers map[string]Timer
}

// New creates a Pool with the given config and the real executors.
// Call Reconcile(ctx) once the daemon is ready to bring the pool to
// desired state.
func New(cfg *Config, exec Executor) *Pool {
	return newWithClock(cfg, exec, realClock{})
}

// newWithClock creates a Pool with an injectable clock (for tests).
func newWithClock(cfg *Config, exec Executor, clk Clock) *Pool {
	return &Pool{
		cfg:          cfg,
		exec:         exec,
		clk:          clk,
		instances:    map[string]*Instance{},
		lingerTimers: map[string]Timer{},
	}
}

// Reconcile walks the catalogue and brings the pool to desired state.
// It creates missing instances and optionally pre-boots up to RunningWarm.
// Should be called on daemon startup and can be called again after any
// release to refill.
func (p *Pool) Reconcile(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.cfg.Templates {
		tmpl := &p.cfg.Templates[i]
		if err := p.reconcileTemplate(ctx, tmpl); err != nil {
			slog.Warn("pool: reconcile template failed",
				"template", tmpl.Name, "error", err)
		}
	}
}

// reconcileTemplate brings one template's pool to desired state. Must be
// called with p.mu held.
func (p *Pool) reconcileTemplate(_ context.Context, tmpl *TemplateConfig) error {
	available, running, _ := p.countsByTemplate(tmpl.Name)
	total := available + running

	// Create instances until we reach AvailableMin (not counting running ones,
	// which are already "more available" than available).
	needed := tmpl.AvailableMin - total
	for i := 0; i < needed; i++ {
		inst, err := p.mintInstance(tmpl)
		if err != nil {
			slog.Warn("pool: mint failed during reconcile",
				"template", tmpl.Name, "error", err)
			break
		}
		slog.Info("pool: minted instance",
			"template", tmpl.Name, "id", inst.ID, "device_id", inst.DeviceID)
	}

	// Re-count after minting.
	available, running, _ = p.countsByTemplate(tmpl.Name)

	// Pre-boot up to RunningWarm instances from the available tier.
	toWarm := tmpl.RunningWarm - running
	if toWarm > available {
		toWarm = available
	}
	for i := 0; i < toWarm; i++ {
		inst := p.pickAvailable(tmpl.Name)
		if inst == nil {
			break
		}
		if err := p.bootInstance(inst); err != nil {
			slog.Warn("pool: pre-boot failed during reconcile",
				"template", tmpl.Name, "id", inst.ID, "error", err)
		}
	}
	return nil
}

// Acquire reserves an instance for the named template. It prefers a running
// instance (near-instant), then boots an available one, then mints+boots a
// new one.
func (p *Pool) Acquire(templateName string) (*Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	tmpl := p.findTemplate(templateName)
	if tmpl == nil {
		return nil, fmt.Errorf("pool: unknown template %q", templateName)
	}

	// Cancel any pending linger timer for an instance we're about to hand off.
	// Prefer running tier first.
	if inst := p.pickRunning(templateName); inst != nil {
		p.cancelLinger(inst.ID)
		inst.Tier = TierReserved
		slog.Info("pool: acquired from running tier",
			"id", inst.ID, "template", templateName)
		return inst, nil
	}

	// Fall back to available tier — boot it.
	if inst := p.pickAvailable(templateName); inst != nil {
		if err := p.bootInstance(inst); err != nil {
			return nil, fmt.Errorf("pool: boot for acquire: %w", err)
		}
		inst.Tier = TierReserved
		slog.Info("pool: acquired from available tier (booted)",
			"id", inst.ID, "template", templateName)
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
	inst.Tier = TierReserved
	slog.Info("pool: acquired freshly-minted instance",
		"id", inst.ID, "template", templateName)
	return inst, nil
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
	inst.LastReleaseAt = p.clk.Now()

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

	_, available, _ := p.countAvailableByTemplate(inst.Template)
	if available >= tmpl.AvailableMax {
		// Cap reached — delete.
		slog.Info("pool: linger expired, available cap reached — deleting",
			"id", instanceID, "template", inst.Template,
			"available", available, "max", tmpl.AvailableMax)
		_ = p.destroyInstance(inst)
		return
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

// countsByTemplate returns (available, running, reserved) for template.
func (p *Pool) countsByTemplate(name string) (available, running, reserved int) {
	for _, inst := range p.instances {
		if inst.Template != name {
			continue
		}
		switch inst.Tier {
		case TierAvailable:
			available++
		case TierRunning:
			running++
		case TierReserved:
			reserved++
		}
	}
	return
}

// countAvailableByTemplate is like countsByTemplate but returns
// (running+reserved, available, total) — useful for cap checks.
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
