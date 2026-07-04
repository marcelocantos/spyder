// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// InstancePool governs the lifecycle of factory-spawned game instances
// (🎯T92.1 clause 2) — the server-medium analogue of the sim/emu pool, but
// kept deliberately separate from it (the sim/emu pool's Executor is
// simctl/avdmanager-shaped; a factory instance is an app-channel session).
// A caller "coming in" (a player, or an agent) Acquires an instance of a
// game from a factory: an idle instance is reused, otherwise a fresh one is
// spawned up to a per-factory capacity cap. Release marks it idle; an idle
// instance is GC'd (asked to quit) after a linger window.
type InstancePool struct {
	mgr           *Manager
	maxPerFactory int
	linger        time.Duration

	mu        sync.Mutex
	instances map[string]*pooledInstance // keyed by instance session id
}

type pooledInstance struct {
	sessionID string
	factoryID string
	game      string
	holder    string // "" when idle/available
}

// InstancePoolOption configures an InstancePool.
type InstancePoolOption func(*InstancePool)

// WithMaxPerFactory caps concurrent instances per factory (default 4).
func WithMaxPerFactory(n int) InstancePoolOption {
	return func(p *InstancePool) {
		if n > 0 {
			p.maxPerFactory = n
		}
	}
}

// WithLinger sets how long an idle instance is kept before GC (default 30s).
func WithLinger(d time.Duration) InstancePoolOption {
	return func(p *InstancePool) { p.linger = d }
}

// NewInstancePool creates an instance pool over a Manager.
func NewInstancePool(mgr *Manager, opts ...InstancePoolOption) *InstancePool {
	p := &InstancePool{
		mgr:           mgr,
		maxPerFactory: 4,
		linger:        30 * time.Second,
		instances:     map[string]*pooledInstance{},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Acquire returns a reserved instance of game from factory, held by holder.
// It reuses an idle instance for the same (factory, game) if one exists,
// otherwise spawns a fresh one — provided the factory is below its capacity
// cap. A player coming in is exactly this call.
func (p *InstancePool) Acquire(ctx context.Context, factory *Session, game, holder string, timeout time.Duration) (*Session, error) {
	if factory == nil {
		return nil, fmt.Errorf("appchannel: acquire: nil factory")
	}

	p.mu.Lock()
	// Reuse an idle instance for this (factory, game).
	for id, pi := range p.instances {
		if pi.factoryID == factory.ID && pi.game == game && pi.holder == "" {
			s, ok := p.mgr.GetSession(id)
			if !ok {
				delete(p.instances, id) // session gone — drop the stale entry
				continue
			}
			pi.holder = holder
			p.mu.Unlock()
			return s, nil
		}
	}
	// Capacity check (count live instances for this factory).
	count := 0
	for id, pi := range p.instances {
		if _, ok := p.mgr.GetSession(id); !ok {
			delete(p.instances, id)
			continue
		}
		if pi.factoryID == factory.ID {
			count++
		}
	}
	if count >= p.maxPerFactory {
		p.mu.Unlock()
		return nil, fmt.Errorf("appchannel: factory %s at capacity (%d/%d instances)",
			factory.ID, count, p.maxPerFactory)
	}
	p.mu.Unlock()

	// Spawn a fresh instance (outside the lock — SpawnInstance blocks on the
	// round-trip + the instance's connect).
	addr := fmt.Sprintf("127.0.0.1:%d", factory.Port)
	inst, err := p.mgr.SpawnInstance(ctx, factory, SpawnRequest{Game: game, AppChannel: addr}, timeout)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.instances[inst.ID] = &pooledInstance{
		sessionID: inst.ID,
		factoryID: factory.ID,
		game:      game,
		holder:    holder,
	}
	p.mu.Unlock()
	return inst, nil
}

// Release marks the instance idle and schedules it for GC after the linger
// window (unless it's re-acquired first).
func (p *InstancePool) Release(sessionID string) {
	p.mu.Lock()
	pi, ok := p.instances[sessionID]
	if !ok {
		p.mu.Unlock()
		return
	}
	pi.holder = ""
	linger := p.linger
	p.mu.Unlock()
	time.AfterFunc(linger, func() { p.gcIfIdle(sessionID) })
}

// gcIfIdle removes and quits an instance if it's still idle when the linger
// timer fires. A re-acquired instance (holder set) is left alone.
func (p *InstancePool) gcIfIdle(sessionID string) {
	p.mu.Lock()
	pi, ok := p.instances[sessionID]
	if !ok || pi.holder != "" {
		p.mu.Unlock()
		return
	}
	delete(p.instances, sessionID)
	p.mu.Unlock()

	// Best-effort: ask the instance to quit. A real ge instance handles quit
	// and exits; a fixture that doesn't just drops out of the pool.
	if s, ok := p.mgr.GetSession(sessionID); ok {
		_, _ = s.Call(context.Background(), MethodQuit, nil, 2*time.Second)
	}
}

// Instances returns a snapshot count of tracked instances per factory id.
// Exposed for status/introspection.
func (p *InstancePool) Instances() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for _, pi := range p.instances {
		out[pi.factoryID]++
	}
	return out
}
