// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package health is the foundation of the daemon health supervisor (🎯T90.1).
// It provides a thread-safe in-memory health model — the single source of
// truth for every monitored entity — and a low-frequency polling seam.
//
// The package has zero dependencies on other internal packages; everything
// else in the supervisor stack will import IT, not the other way around.
package health

import (
	"sort"
	"sync"
	"time"
)

// Kind classifies what a monitored entity represents.
type Kind string

const (
	KindDaemon     Kind = "daemon"
	KindSubprocess Kind = "subprocess"
	KindDevice     Kind = "device"
)

// State is the health state of a monitored entity as determined by the
// state machine. Recovery escalation (Degraded → NeedsAttention) is
// driven exclusively by exhausting MaxAttempts, NOT by raw probe failures,
// so repeated Observe(false) calls can never inflate severity on their own.
type State string

const (
	Healthy          State = "healthy"
	Degraded         State = "degraded"          // failing; recovery not yet exhausted
	Recovering       State = "recovering"        // a recovery attempt is in progress
	NeedsAttention   State = "needs_attention"   // recovery exhausted — requires human intervention
	AbsentExpected   State = "absent_expected"   // gone, and its absence is acceptable
	AbsentUnexpected State = "absent_unexpected" // gone unexpectedly
)

// maxEvidence caps the evidence ring at 10 observations per entity.
// The model is entirely in-memory; callers that need durable history should
// subscribe via OnTransition and persist outside this package.
const maxEvidence = 10

// ID uniquely identifies a monitored entity. The zero value is invalid.
type ID struct {
	Kind  Kind   `json:"kind"`
	Name  string `json:"name"`
	Layer string `json:"layer,omitempty"` // device stack layer; empty for non-device entities
}

// Observation is one recorded probe or event result for an entity.
type Observation struct {
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail"`
}

// Policy is an entity's recovery policy. A zero Policy means "no automatic
// recovery": MaxAttempts==0 prevents RecoveryFailed from ever graduating
// to NeedsAttention (the entity stays Recovering indefinitely).
type Policy struct {
	MaxAttempts int           // transitions to NeedsAttention after this many RecoveryFailed calls; 0 = no limit
	BaseBackoff time.Duration // backoff base; NextBackoff = BaseBackoff * 2^(attempts-1)
}

// entity is the internal, mutable per-entity record. Public reads go through
// EntitySnapshot (deep copy) so callers never hold a pointer into the model.
type entity struct {
	id        ID
	kind      Kind
	state     State
	policy    Policy
	attempts  int
	lastProbe time.Time
	evidence  []Observation // bounded ring; most-recent maxEvidence entries
}

// appendEvidence appends obs to e.evidence and trims to the most recent
// maxEvidence entries. Oldest entries are dropped first.
func (e *entity) appendEvidence(obs Observation) {
	e.evidence = append(e.evidence, obs)
	if len(e.evidence) > maxEvidence {
		e.evidence = e.evidence[len(e.evidence)-maxEvidence:]
	}
}

// Transition is emitted to OnTransition observers whenever an entity's
// state changes. It is fired AFTER the model lock is released to avoid
// re-entrancy deadlocks in observers that themselves call model methods.
type Transition struct {
	ID   ID
	From State
	To   State
	At   time.Time
}

// Model is the thread-safe in-memory health model. Construct with New.
type Model struct {
	mu       sync.Mutex
	entities map[ID]*entity
	now      func() time.Time   // injectable clock for deterministic tests
	onTrans  []func(Transition) // observers notified after every state change
}

// Option configures a Model at construction time.
type Option func(*Model)

// WithClock injects a custom clock function. Use in tests to drive time
// forward deterministically.
func WithClock(fn func() time.Time) Option {
	return func(m *Model) {
		m.now = fn
	}
}

// New constructs a Model. With no options, it uses time.Now as its clock.
func New(opts ...Option) *Model {
	m := &Model{
		entities: make(map[ID]*entity),
		now:      time.Now,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// OnTransition registers fn as a transition observer. fn is called
// outside the model lock, after each state change. Observers must not
// block indefinitely.
func (m *Model) OnTransition(fn func(Transition)) {
	m.mu.Lock()
	m.onTrans = append(m.onTrans, fn)
	m.mu.Unlock()
}

// fire calls every registered transition observer. Must be called with
// the model lock NOT held.
func (m *Model) fire(t Transition) {
	for _, fn := range m.onTrans {
		fn(t)
	}
}

// Register creates the entity in state Healthy if it does not exist
// (idempotent — existing entity keeps its current state and has its
// policy updated). A synthetic From="" → To=Healthy transition is always
// fired so subscribers learn about newly registered entities.
func (m *Model) Register(id ID, kind Kind, policy Policy) {
	m.mu.Lock()
	now := m.now()
	e, exists := m.entities[id]
	if !exists {
		e = &entity{id: id, kind: kind, state: Healthy, policy: policy}
		m.entities[id] = e
	} else {
		e.policy = policy
	}
	e.attempts = 0
	t := Transition{ID: id, From: "", To: Healthy, At: now}
	if exists {
		// Entity already existed: report its current state as the "from"
		// so observers see what it was before re-registration.
		t.From = e.state
		t.To = e.state
	}
	m.mu.Unlock()
	m.fire(t)
}

// Observe records one probe or liveness result for the entity. If the
// entity is unknown it is auto-registered with a zero Policy. The state
// machine advances as follows:
//   - ok==true  → Healthy (always), attempts reset to 0
//   - ok==false → Degraded only when currently Healthy or Absent*;
//     stays unchanged from Degraded/Recovering/NeedsAttention (escalation
//     is driven by recovery exhaustion, not raw observation counts).
func (m *Model) Observe(id ID, ok bool, detail string) {
	m.mu.Lock()
	now := m.now()

	e := m.ensureEntity(id)
	obs := Observation{At: now, OK: ok, Detail: detail}
	e.appendEvidence(obs)
	e.lastProbe = now

	var t *Transition
	switch {
	case ok:
		if e.state != Healthy {
			t = &Transition{ID: id, From: e.state, To: Healthy, At: now}
			e.state = Healthy
			e.attempts = 0
		}
	default:
		// Only transition to Degraded from states that don't represent an
		// active degraded/recovery path — repeated failures on an already-
		// degraded entity must not reset the recovery counter or bypass the
		// NeedsAttention path.
		switch e.state {
		case Healthy, AbsentExpected, AbsentUnexpected:
			t = &Transition{ID: id, From: e.state, To: Degraded, At: now}
			e.state = Degraded
		}
	}
	m.mu.Unlock()

	if t != nil {
		m.fire(*t)
	}
}

// RecoveryStarted transitions the entity to Recovering from any non-Healthy
// state. Call this when a recovery action (daemon restart, tunnel re-dial,
// …) is kicked off.
func (m *Model) RecoveryStarted(id ID) {
	m.mu.Lock()
	now := m.now()
	e := m.ensureEntity(id)
	var t *Transition
	// Only a genuine change fires: Healthy has nothing to recover, and an
	// already-Recovering entity must not emit a spurious self-transition.
	if e.state != Healthy && e.state != Recovering {
		t = &Transition{ID: id, From: e.state, To: Recovering, At: now}
		e.state = Recovering
	}
	m.mu.Unlock()

	if t != nil {
		m.fire(*t)
	}
}

// RecoveryFailed records a failed recovery attempt. If attempts reach
// policy.MaxAttempts (and MaxAttempts > 0) the entity moves to
// NeedsAttention; otherwise it returns to Degraded awaiting the next
// attempt. MaxAttempts==0 means no automatic give-up: the entity stays
// Recovering so the supervisor can retry indefinitely.
func (m *Model) RecoveryFailed(id ID, detail string) {
	m.mu.Lock()
	now := m.now()
	e := m.ensureEntity(id)
	e.attempts++
	obs := Observation{At: now, OK: false, Detail: detail}
	e.appendEvidence(obs)

	var next State
	if e.policy.MaxAttempts > 0 && e.attempts >= e.policy.MaxAttempts {
		next = NeedsAttention
	} else if e.policy.MaxAttempts <= 0 {
		// No give-up limit: stay Recovering so the supervisor keeps trying.
		next = Recovering
	} else {
		next = Degraded
	}
	t := Transition{ID: id, From: e.state, To: next, At: now}
	e.state = next
	m.mu.Unlock()

	m.fire(t)
}

// RecoverySucceeded transitions the entity to Healthy and resets attempts.
// Can recover even from NeedsAttention.
func (m *Model) RecoverySucceeded(id ID) {
	m.mu.Lock()
	now := m.now()
	e := m.ensureEntity(id)
	t := Transition{ID: id, From: e.state, To: Healthy, At: now}
	e.state = Healthy
	e.attempts = 0
	m.mu.Unlock()

	m.fire(t)
}

// MarkAbsent records that the entity has disappeared. expected==true means
// the absence is benign (e.g. a device was intentionally disconnected);
// expected==false means it vanished unexpectedly.
func (m *Model) MarkAbsent(id ID, expected bool, detail string) {
	m.mu.Lock()
	now := m.now()
	e := m.ensureEntity(id)
	obs := Observation{At: now, OK: false, Detail: detail}
	e.appendEvidence(obs)
	e.lastProbe = now
	e.attempts = 0

	next := AbsentUnexpected
	if expected {
		next = AbsentExpected
	}
	t := Transition{ID: id, From: e.state, To: next, At: now}
	e.state = next
	m.mu.Unlock()

	m.fire(t)
}

// NextBackoff returns the exponential backoff duration for the entity's
// next recovery attempt: BaseBackoff * 2^(attempts-1) for attempts≥1,
// or BaseBackoff for attempts==0. Returns 0 if the entity is unknown or
// BaseBackoff is 0.
func (m *Model) NextBackoff(id ID) time.Duration {
	m.mu.Lock()
	e, ok := m.entities[id]
	if !ok || e.policy.BaseBackoff == 0 {
		m.mu.Unlock()
		return 0
	}
	attempts := e.attempts
	base := e.policy.BaseBackoff
	m.mu.Unlock()

	if attempts <= 0 {
		return base
	}
	// 2^(attempts-1) computed with integer arithmetic to avoid float64.
	// Cap the shift so a nonsensical attempt count can't overflow int64.
	shift := min(attempts-1, 62)
	return base * (1 << uint(shift))
}

// ensureEntity returns the entity for id, auto-registering with a zero
// Policy if it does not exist. Must be called with m.mu held.
func (m *Model) ensureEntity(id ID) *entity {
	e, ok := m.entities[id]
	if !ok {
		e = &entity{id: id, kind: id.Kind, state: Healthy}
		m.entities[id] = e
	}
	return e
}

// EntitySnapshot is a point-in-time, deep-copy view of one entity.
// All fields are exported and JSON-serialisable for the T90.3 query surface.
type EntitySnapshot struct {
	ID        ID            `json:"id"`
	Kind      Kind          `json:"kind"`
	State     State         `json:"state"`
	Attempts  int           `json:"attempts"`
	LastProbe time.Time     `json:"last_probe"`
	Evidence  []Observation `json:"evidence"`
}

// Snapshot is a point-in-time view of the entire model. Entities are
// sorted by (Kind, Name, Layer) for deterministic output.
type Snapshot struct {
	At       time.Time        `json:"at"`
	Entities []EntitySnapshot `json:"entities"`
}

// Snapshot returns a deep copy of the current model state. The Entities
// slice is sorted by (Kind, Name, Layer) so JSON output is stable.
func (m *Model) Snapshot() Snapshot {
	m.mu.Lock()
	now := m.now()
	snap := Snapshot{At: now, Entities: make([]EntitySnapshot, 0, len(m.entities))}
	for _, e := range m.entities {
		ev := make([]Observation, len(e.evidence))
		copy(ev, e.evidence)
		snap.Entities = append(snap.Entities, EntitySnapshot{
			ID:        e.id,
			Kind:      e.kind,
			State:     e.state,
			Attempts:  e.attempts,
			LastProbe: e.lastProbe,
			Evidence:  ev,
		})
	}
	m.mu.Unlock()

	sort.Slice(snap.Entities, func(i, j int) bool {
		a, b := snap.Entities[i].ID, snap.Entities[j].ID
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Layer < b.Layer
	})
	return snap
}

// Get returns a snapshot of one entity. ok is false if the entity is unknown.
func (m *Model) Get(id ID) (EntitySnapshot, bool) {
	m.mu.Lock()
	e, ok := m.entities[id]
	if !ok {
		m.mu.Unlock()
		return EntitySnapshot{}, false
	}
	ev := make([]Observation, len(e.evidence))
	copy(ev, e.evidence)
	snap := EntitySnapshot{
		ID:        e.id,
		Kind:      e.kind,
		State:     e.state,
		Attempts:  e.attempts,
		LastProbe: e.lastProbe,
		Evidence:  ev,
	}
	m.mu.Unlock()
	return snap, true
}
