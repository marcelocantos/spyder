// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package agent supervises one native XCTest runner per tethered iOS
// device. The state machine is pure: USB, testmanagerd, and process
// I/O enter only through Probe and Effects, so tests drive every
// transition without a device.
package agent

import (
	"errors"
	"fmt"
	"sync"
)

// Runner bundles Spyder's installed XCTest agent.
const (
	RunnerBundle = "com.marcelocantos.spyder.AlertRunner.xctrunner"
	HostBundle   = "com.marcelocantos.spyder.AlertHost"
	// HoldTest is the resident XCTest that stays up so later clients
	// reuse the process instead of launching another runner.
	HoldTest = "AlertRunner.SystemAlertTests/testHoldAgent"
)

// MaxAttempts is the hard cap on relaunch and consent re-prompt
// attempts. Past this, supervision stops and does not report ready.
const MaxAttempts = 3

// Condition is the single bounded supervision state of one device.
type Condition string

const (
	CondStartup           Condition = "startup"
	CondReady             Condition = "ready"
	CondIdle              Condition = "idle"
	CondAuthorizationLost Condition = "authorization_lost"
	CondLocked            Condition = "locked"
	CondDisconnected      Condition = "disconnected"
	CondReconnected       Condition = "reconnected"
	CondRebooted          Condition = "rebooted"
	CondAgentFailure      Condition = "agent_failure"
	CondConsentRequired   Condition = "consent_required"
	CondRetryExhausted    Condition = "retry_exhausted"
)

// Mutation is an input or app-lifecycle change that requires the
// per-device owner.
type Mutation string

const (
	MutInput     Mutation = "input"
	MutLifecycle Mutation = "app_lifecycle"
)

// ErrNotOwner is returned when a caller does not hold the device.
var ErrNotOwner = errors.New("caller does not hold per-device automation ownership")

// ErrConsent is returned by Launch when Apple is asking the owner to
// allow UI automation. Supervision surfaces that; it does not dismiss it.
var ErrConsent = errors.New("apple UI automation consent is required")

// Runner is the identity of the on-device XCTest agent.
type Runner struct {
	BundleID   string `json:"bundle_id"`
	PID        int    `json:"pid"`
	InstallGen string `json:"install_gen"`
}

// Consent is Apple's UI-automation prompt. It is set only while the
// owner can act on it.
type Consent struct {
	Actionable bool   `json:"actionable"`
	Message    string `json:"message"`
}

// Snapshot is one device's supervision state.
type Snapshot struct {
	DeviceID      string    `json:"device_id"`
	Condition     Condition `json:"condition"`
	Runner        Runner    `json:"runner"`
	RunnerHealthy bool      `json:"runner_healthy"`
	AUTBundle     string    `json:"aut_bundle,omitempty"`
	AUTRunning    bool      `json:"aut_running"`
	LeaseHeld     bool      `json:"lease_held"`
	HostSession   bool      `json:"host_session"`
	Owner         string    `json:"owner,omitempty"`
	Ready         bool      `json:"ready"`
	BootID        string    `json:"boot_id,omitempty"`
	Consent       *Consent  `json:"consent,omitempty"`
	Attempts      int       `json:"attempts"`
	Relaunched    bool      `json:"relaunched"`
	Reinstalled   bool      `json:"reinstalled"`
	Recovered     bool      `json:"recovered"`
}

// Observation is what a probe saw. The machine does not collect it.
type Observation struct {
	Tethered bool
	Locked   bool
	// LockKnown is false when the lock read failed. That is not an
	// unlock: supervision must not report ready, and it must not
	// treat the failure as CondLocked.
	LockKnown         bool
	BootID            string
	RunnerPID         int
	RunnerAlive       bool
	Authorized        bool
	ConsentRequired   bool
	ConsentActionable bool
	ConsentMessage    string
	Installed         bool
	InstallGen        string
	AUTBundle         string
	AUTRunning        bool
	HostSession       bool
	LeaseHeld         bool
}

// Effects are the only side effects supervision performs.
type Effects struct {
	Launch          func(deviceID string) (pid int, err error)
	Install         func(deviceID string) (installGen string, err error)
	StopAUT         func(deviceID, bundle string) error
	DropHostSession func(deviceID string) error
	DropLease       func(deviceID string) error
	// ResetConsent forgets a recorded consent failure so the next pass
	// may launch again (🎯T175). It runs with the supervisor lock held
	// and must not call back into the supervisor.
	ResetConsent func(deviceID string)
}

// SupervisorArgs configures a Supervisor. Zero MaxAttempts selects
// MaxAttempts. A nil Probe or OwnerOf makes Ensure fail closed.
type SupervisorArgs struct {
	RunnerBundle string
	// RunnerBundleFor selects the runner per device when backends differ
	// (🎯T175). Empty results fall back to RunnerBundle.
	RunnerBundleFor func(deviceID string) string
	MaxAttempts     int
	Effects         Effects
	Probe           func(deviceID string) (Observation, error)
	// OwnerOf reports the reservation holder. ok is false when nobody
	// holds the device.
	OwnerOf func(deviceID string) (owner string, ok bool)
}

// Supervisor is the shipped per-device XCTest agent supervisor.
type Supervisor struct {
	mu           sync.Mutex
	runnerBundle string
	bundleFor    func(deviceID string) string
	max          int
	effects      Effects
	probe        func(deviceID string) (Observation, error)
	ownerOf      func(deviceID string) (owner string, ok bool)
	devices      map[string]*machine
}

type machine struct {
	id         string
	condition  Condition
	runner     Runner
	healthy    bool
	autBundle  string
	autRunning bool
	lease      bool
	host       bool
	owner      string
	ready      bool
	consent    *Consent
	attempts   int
	bootID     string
	idle       bool
	recovered  bool
}

// NewSupervisor returns a supervisor whose devices start at startup
// and are not ready.
func NewSupervisor(args *SupervisorArgs) *Supervisor {
	if args == nil {
		args = &SupervisorArgs{}
	}
	max := args.MaxAttempts
	if max <= 0 {
		max = MaxAttempts
	}
	bundle := args.RunnerBundle
	if bundle == "" {
		bundle = RunnerBundle
	}
	return &Supervisor{
		runnerBundle: bundle,
		bundleFor:    args.RunnerBundleFor,
		max:          max,
		effects:      args.Effects,
		probe:        args.Probe,
		ownerOf:      args.OwnerOf,
		devices:      map[string]*machine{},
	}
}

func (s *Supervisor) bundle(id string) string {
	if s.bundleFor != nil {
		if b := s.bundleFor(id); b != "" {
			return b
		}
	}
	return s.runnerBundle
}

// freshBudget clears the attempt cap and any recorded consent failure.
// A reboot, a new daemon process, and an attended Retry are the only
// events that call it, so an unattended device is asked at most
// MaxAttempts times per boot (🎯T175).
func (s *Supervisor) freshBudget(m *machine) {
	m.attempts = 0
	m.consent = nil
	if s.effects.ResetConsent != nil {
		s.effects.ResetConsent(m.id)
	}
}

func (s *Supervisor) machine(id string) *machine {
	m := s.devices[id]
	if m == nil {
		m = &machine{id: id, condition: CondStartup}
		s.devices[id] = m
	}
	return m
}

// HasOwner reports whether a client has taken the supervised session.
func (s *Supervisor) HasOwner(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.devices[deviceID]
	return m != nil && m.owner != ""
}

// Snapshot returns the last state for a device.
func (s *Supervisor) Snapshot(deviceID string) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.devices[deviceID]
	if m == nil {
		return Snapshot{}, false
	}
	return m.snapshot(false, false), true
}

func (m *machine) snapshot(relaunched, reinstalled bool) Snapshot {
	var consent *Consent
	if m.consent != nil {
		c := *m.consent
		consent = &c
	}
	return Snapshot{
		DeviceID:      m.id,
		Condition:     m.condition,
		Runner:        m.runner,
		RunnerHealthy: m.healthy,
		AUTBundle:     m.autBundle,
		AUTRunning:    m.autRunning,
		LeaseHeld:     m.lease,
		HostSession:   m.host,
		Owner:         m.owner,
		Ready:         m.ready,
		BootID:        m.bootID,
		Consent:       consent,
		Attempts:      m.attempts,
		Relaunched:    relaunched,
		Reinstalled:   reinstalled,
		Recovered:     m.recovered,
	}
}

func (s *Supervisor) holds(deviceID, owner string) error {
	if s.ownerOf == nil {
		return ErrNotOwner
	}
	holder, ok := s.ownerOf(deviceID)
	if !ok || holder == "" || owner == "" || holder != owner {
		return fmt.Errorf("%w", ErrNotOwner)
	}
	return nil
}

// Ensure admits an authorized client. A healthy runner is reused. A
// dead or unauthorized runner is the only case that relaunches, and
// only while the device is tethered, unlocked, and not waiting on consent.
func (s *Supervisor) Ensure(deviceID, owner string) (Snapshot, error) {
	if err := s.holds(deviceID, owner); err != nil {
		return Snapshot{}, err
	}
	obs, err := s.observe(deviceID)
	if err != nil {
		return Snapshot{}, err
	}
	// The probe can take seconds. A reservation released meanwhile must
	// not start the runner for the previous holder.
	if err := s.holds(deviceID, owner); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machine(deviceID)
	m.owner = owner
	m.idle = false
	return s.advance(m, obs, true)
}

// Retry is an attended Ensure: the reservation holder is at the device
// and asks for a fresh attempt budget after RetryExhausted or a consent
// failure (🎯T175). Plain Ensure keeps the cap.
func (s *Supervisor) Retry(deviceID, owner string) (Snapshot, error) {
	if err := s.holds(deviceID, owner); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	s.freshBudget(s.machine(deviceID))
	s.mu.Unlock()
	return s.Ensure(deviceID, owner)
}

// Tick is the tether and liveness pass. It restarts only from a
// non-healthy runner, and only within the attempt cap.
func (s *Supervisor) Tick(deviceID string) (Snapshot, error) {
	obs, err := s.observe(deviceID)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machine(deviceID)
	snap, err := s.advance(m, obs, true)
	if err != nil {
		return snap, err
	}
	// A changed boot id is a reboot on this tick even when the device
	// is idle. idleLocked would replace CondRebooted with CondIdle
	// after the new id had already been absorbed, so the next tick
	// would not report the reboot either.
	if snap.Condition == CondRebooted {
		return snap, nil
	}
	if m.idle {
		return s.idleLocked(m)
	}
	return snap, nil
}

// Idle stops the app under test and drops any build lease and any
// host xcodebuild or testmanagerd session. A healthy runner is left
// in place. It probes first so the bundle launch_app recorded after
// the last ensure is the one that gets terminated.
func (s *Supervisor) Idle(deviceID, owner string) (Snapshot, error) {
	if err := s.holds(deviceID, owner); err != nil {
		return Snapshot{}, err
	}
	obs, obsErr := s.observe(deviceID)
	if err := s.holds(deviceID, owner); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machine(deviceID)
	m.owner = owner
	if obsErr == nil {
		s.absorb(m, obs)
	}
	return s.idleLocked(m)
}

func (s *Supervisor) idleLocked(m *machine) (Snapshot, error) {
	m.idle = true
	bundle := m.autBundle
	id := m.id
	s.mu.Unlock()
	var stopErr, hostErr, leaseErr error
	if s.effects.StopAUT != nil && bundle != "" {
		stopErr = s.effects.StopAUT(id, bundle)
	}
	if s.effects.DropHostSession != nil {
		hostErr = s.effects.DropHostSession(id)
	}
	if s.effects.DropLease != nil {
		leaseErr = s.effects.DropLease(id)
	}
	s.mu.Lock()
	if stopErr == nil {
		m.autRunning = false
	}
	if hostErr == nil {
		m.host = false
	}
	if leaseErr == nil {
		m.lease = false
	}
	m.condition = CondIdle
	// Idle is not a client session. Ready stays false so a failure
	// cannot be reported as ready; RunnerHealthy still records the agent.
	m.ready = false
	m.recovered = false
	err := errors.Join(stopErr, hostErr, leaseErr)
	return m.snapshot(false, false), err
}

// Mutate rejects a caller that does not hold the device. Input and
// app-lifecycle mutations both go through this check.
func (s *Supervisor) Mutate(deviceID, owner string, kind Mutation) error {
	if kind != MutInput && kind != MutLifecycle {
		return fmt.Errorf("unknown automation mutation %q", kind)
	}
	if err := s.holds(deviceID, owner); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.devices[deviceID]
	if m != nil && m.owner != "" && m.owner != owner {
		return fmt.Errorf("%w", ErrNotOwner)
	}
	return nil
}

// NoteTether is the edge the USB listener calls. A reconnect is ready
// only when the probe shows a healthy authorized runner.
func (s *Supervisor) NoteTether(deviceID string, up bool) (Snapshot, error) {
	if !up {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.applyLocked(s.machine(deviceID), CondDisconnected, Observation{}), nil
	}
	obs, err := s.observe(deviceID)
	if err != nil {
		obs = Observation{}
	}
	obs.Tethered = true
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(s.machine(deviceID), CondReconnected, obs), nil
}

// Apply lands a named condition in exactly one bounded state.
func (s *Supervisor) Apply(deviceID string, c Condition, obs Observation) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(s.machine(deviceID), c, obs)
}

func (s *Supervisor) applyLocked(m *machine, c Condition, obs Observation) Snapshot {
	s.absorb(m, obs)
	m.condition = c
	m.recovered = false
	m.ready = false
	switch c {
	case CondReconnected:
		if obs.Tethered && obs.RunnerAlive && obs.Authorized && obs.LockKnown && !obs.Locked && !obs.ConsentRequired {
			m.ready = true
			m.recovered = true
			m.healthy = true
		} else {
			m.ready = false
			m.recovered = false
		}
	case CondRebooted:
		m.runner.PID = 0
		m.healthy = false
		m.ready = false
	case CondConsentRequired:
		m.ready = false
		if obs.ConsentRequired && obs.ConsentActionable {
			m.consent = &Consent{Actionable: true, Message: obs.ConsentMessage}
		} else {
			m.consent = nil
		}
	case CondReady:
		m.ready = m.healthy && obs.Authorized && obs.Tethered && obs.LockKnown && !obs.Locked
	default:
		m.ready = false
	}
	return m.snapshot(false, false)
}

func (s *Supervisor) absorb(m *machine, obs Observation) {
	if obs.BootID != "" && m.bootID != "" && obs.BootID != m.bootID {
		m.condition = CondRebooted
		m.runner.PID = 0
		m.healthy = false
		m.ready = false
		// Apple asks again after a reboot. Give that boot its own budget.
		s.freshBudget(m)
	}
	if obs.BootID != "" {
		m.bootID = obs.BootID
	}
	if obs.AUTBundle != "" {
		m.autBundle = obs.AUTBundle
	}
	m.autRunning = obs.AUTRunning
	m.lease = obs.LeaseHeld
	m.host = obs.HostSession
	if obs.RunnerAlive {
		m.runner = Runner{BundleID: s.bundle(m.id), PID: obs.RunnerPID, InstallGen: obs.InstallGen}
		m.healthy = obs.Authorized && !obs.ConsentRequired
	} else if obs.InstallGen != "" {
		m.runner.InstallGen = obs.InstallGen
		m.runner.BundleID = s.bundle(m.id)
		m.healthy = false
	}
}

func (s *Supervisor) advance(m *machine, obs Observation, relaunch bool) (Snapshot, error) {
	if obs.BootID != "" && m.bootID != "" && obs.BootID != m.bootID {
		// Stay rebooted for this observation. Falling through relaunched
		// the runner and returned CondReady on the same call.
		return s.applyLocked(m, CondRebooted, obs), nil
	}
	s.absorb(m, obs)
	switch {
	case !obs.Tethered:
		return s.applyLocked(m, CondDisconnected, obs), nil
	case obs.Locked:
		return s.applyLocked(m, CondLocked, obs), nil
	case !obs.LockKnown:
		return s.applyLocked(m, CondAgentFailure, obs), nil
	case obs.ConsentRequired:
		return s.noteConsent(m, obs)
	case obs.RunnerAlive && obs.Authorized:
		m.condition = CondReady
		m.ready = true
		m.healthy = true
		m.consent = nil
		m.attempts = 0
		m.recovered = false
		m.runner = Runner{BundleID: s.bundle(m.id), PID: obs.RunnerPID, InstallGen: obs.InstallGen}
		return m.snapshot(false, false), nil
	case relaunch && (!obs.RunnerAlive || !obs.Authorized):
		return s.relaunch(m, obs)
	default:
		if !obs.Authorized {
			return s.applyLocked(m, CondAuthorizationLost, obs), nil
		}
		return s.applyLocked(m, CondAgentFailure, obs), nil
	}
}

func (s *Supervisor) noteConsent(m *machine, obs Observation) (Snapshot, error) {
	if m.attempts >= s.max {
		m.condition = CondRetryExhausted
		m.ready = false
		m.healthy = false
		m.consent = nil
		return m.snapshot(false, false), nil
	}
	m.attempts++
	m.condition = CondConsentRequired
	m.ready = false
	m.healthy = false
	if obs.ConsentActionable {
		msg := obs.ConsentMessage
		if msg == "" {
			msg = ErrConsent.Error()
		}
		m.consent = &Consent{Actionable: true, Message: msg}
	} else {
		m.consent = nil
	}
	return m.snapshot(false, false), nil
}

func (s *Supervisor) relaunch(m *machine, obs Observation) (Snapshot, error) {
	if m.attempts >= s.max {
		m.condition = CondRetryExhausted
		m.ready = false
		m.healthy = false
		m.consent = nil
		return m.snapshot(false, false), nil
	}
	id := m.id
	reinstalled := false
	if !obs.Installed && s.effects.Install != nil {
		s.mu.Unlock()
		gen, err := s.effects.Install(id)
		s.mu.Lock()
		if err != nil {
			m.attempts++
			m.condition = CondAgentFailure
			m.ready = false
			m.healthy = false
			return m.snapshot(false, false), nil
		}
		reinstalled = true
		obs.InstallGen = gen
		obs.Installed = true
		m.runner.InstallGen = gen
	}
	if s.effects.Launch == nil {
		m.attempts++
		m.condition = CondAgentFailure
		m.ready = false
		return m.snapshot(false, reinstalled), nil
	}
	s.mu.Unlock()
	pid, err := s.effects.Launch(id)
	s.mu.Lock()
	if err != nil {
		if errors.Is(err, ErrConsent) {
			obs.ConsentRequired = true
			obs.ConsentActionable = true
			if obs.ConsentMessage == "" {
				obs.ConsentMessage = err.Error()
			}
			snap, cerr := s.noteConsent(m, obs)
			snap.Reinstalled = reinstalled
			return snap, cerr
		}
		m.attempts++
		if m.attempts >= s.max {
			m.condition = CondRetryExhausted
			m.consent = nil
		} else {
			m.condition = CondAgentFailure
		}
		m.ready = false
		m.healthy = false
		return m.snapshot(true, reinstalled), nil
	}
	m.attempts = 0
	m.condition = CondReady
	m.ready = true
	m.healthy = true
	m.consent = nil
	m.runner = Runner{BundleID: s.bundle(m.id), PID: pid, InstallGen: obs.InstallGen}
	return m.snapshot(true, reinstalled), nil
}

func (s *Supervisor) observe(deviceID string) (Observation, error) {
	if s.probe == nil {
		return Observation{}, errors.New("automation probe is not configured")
	}
	return s.probe(deviceID)
}
