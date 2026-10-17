// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/danielpaulus/go-ios/ios"
)

// poolEntry is one cached service connection. The conn is held open
// across operations; the per-entry permit serialises operations on it
// (most go-ios service connections are not safe for concurrent use).
type poolEntry[T any] struct {
	conn       T
	generation uint64
	permit     chan struct{}
	lastUsed   time.Time // guarded by ServicePool.mu
	busy       bool      // guarded by ServicePool.mu
	retired    bool      // guarded by ServicePool.mu
	closeOnce  sync.Once
}

type serviceSessionResolver interface {
	Session(string) (ios.DeviceEntry, error)
}

// ServicePool caches one go-ios service connection per (UDID, service)
// for a configurable idle TTL. Reduces usbmuxd session churn by reusing
// open connections across operations — particularly important on
// macOS where rapid open/close of service connections to the same
// device appears to wedge usbmuxd's per-device session table (🎯T67).
//
// Concurrency: ServicePool is safe for concurrent use. Acquire blocks
// only on the per-entry permit while another caller holds the same
// connection; different (UDID, service) tuples are independent.
//
// Generic over the connection type so each service (installationproxy,
// appservice, screenshotr, etc.) can have its own typed pool without
// reflection. The factory + closer functions handle the type-specific
// open/close plumbing.
type ServicePool[T any] struct {
	resolver serviceSessionResolver
	newConn  func(ios.DeviceEntry) (T, error)
	closeFn  func(T) error
	idleTTL  time.Duration

	mu      sync.Mutex
	entries map[string]*poolEntry[T] // key: "udid"
	stop    chan struct{}
	stopped bool
	// opens counts how many times newConn was invoked across the pool's
	// lifetime — i.e. the number of underlying service-channel handshakes.
	// Exposed for tests that want to assert the pool is actually
	// amortising opens (🎯T67's acceptance is about reducing usbmuxd
	// session churn; this is the direct signal).
	opens          int64
	nextGeneration uint64
}

// NewServicePool constructs a pool wrapping the per-device service
// open/close pair. idleTTL bounds how long an idle connection is kept
// alive; a sweeper goroutine evicts older entries every TTL/2.
func NewServicePool[T any](r serviceSessionResolver, newConn func(ios.DeviceEntry) (T, error), closeFn func(T) error, idleTTL time.Duration) *ServicePool[T] {
	if idleTTL <= 0 {
		idleTTL = 60 * time.Second
	}
	p := &ServicePool[T]{
		resolver: r,
		newConn:  newConn,
		closeFn:  closeFn,
		idleTTL:  idleTTL,
		entries:  map[string]*poolEntry[T]{},
		stop:     make(chan struct{}),
	}
	go p.sweep()
	return p
}

// Acquire returns a connection for the given UDID together with a
// release function. The caller MUST call release when done; the
// per-entry permit is held until release fires. The returned T is
// safe to use until release.
//
// On first use for a UDID (or after an Invalidate or TTL eviction),
// Acquire opens a new connection via the pool's newConn factory.
// Session resolve + newConn run WITHOUT holding the pool mutex so a
// wedged handshake for one UDID cannot stall Acquire for others (🎯T99.2).
func (p *ServicePool[T]) Acquire(udid string) (T, func(), error) {
	return p.AcquireContext(context.Background(), udid)
}

// ServiceLease owns one exact connection generation. Invalidate may be used
// after an RPC error even if cancellation already retired/replaced the entry.
type ServiceLease[T any] struct {
	Conn       T
	Release    func()
	Invalidate func()
}

// AcquireContext is AcquireLease with only its connection/release exposed.
func (p *ServicePool[T]) AcquireContext(ctx context.Context, udid string) (T, func(), error) {
	lease, err := p.AcquireLease(ctx, udid)
	if err != nil {
		var zero T
		return zero, func() {}, err
	}
	return lease.Conn, lease.Release, nil
}

// AcquireLease cancels an entry wait without disturbing its current owner.
// After acquisition, cancellation retires and closes exactly that connection
// generation to unblock an established RPC read. Resolver/service construction
// remains upstream synchronous I/O: cancellation is checked around, but cannot
// interrupt, those opaque setup calls.
func (p *ServicePool[T]) AcquireLease(ctx context.Context, udid string) (*ServiceLease[T], error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return nil, errPoolClosed
		}
		e := p.entries[udid]
		p.mu.Unlock()
		if e == nil {
			p.mu.Lock()
			p.nextGeneration++
			generation := p.nextGeneration
			p.mu.Unlock()
			ended := LifecyclePhase(ctx, "service.resolve", "service", reflect.TypeFor[T]().String(), "device_id", udid, "setup_attempt", generation)
			var dev ios.DeviceEntry
			var err error
			if resolver, ok := p.resolver.(interface {
				SessionContext(context.Context, string) (ios.DeviceEntry, error)
			}); ok {
				dev, err = resolver.SessionContext(ctx, udid)
			} else {
				dev, err = p.resolver.Session(udid)
			}
			ended(err)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ended = LifecyclePhase(ctx, "service.factory", "service", reflect.TypeFor[T]().String(), "device_id", udid, "setup_attempt", generation)
			c, err := p.newConn(dev)
			ended(err)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				ended = LifecyclePhase(ctx, "service.late_cancel_close", "service", reflect.TypeFor[T]().String(), "device_id", udid, "setup_attempt", generation)
				closeErr := p.closeFn(c)
				ended(closeErr)
				return nil, err
			}
			candidate := &poolEntry[T]{conn: c, generation: generation, lastUsed: time.Now(), permit: make(chan struct{}, 1)}
			candidate.permit <- struct{}{}
			p.mu.Lock()
			if p.stopped {
				p.mu.Unlock()
				_ = p.closeFn(c)
				return nil, errPoolClosed
			}
			e = p.entries[udid]
			if e == nil {
				e = candidate
				p.entries[udid] = e
				p.opens++
			}
			p.mu.Unlock()
			if e != candidate {
				_ = p.closeFn(c)
			}
		}
		ended := LifecyclePhase(ctx, "service.permit_wait", "service", reflect.TypeFor[T]().String(), "device_id", udid, "connection_generation", e.generation)
		select {
		case <-ctx.Done():
			ended(ctx.Err())
			return nil, ctx.Err()
		case <-e.permit:
			ended(nil)
		}
		p.mu.Lock()
		if e.retired {
			p.mu.Unlock()
			e.permit <- struct{}{}
			continue
		}
		e.busy = true
		e.lastUsed = time.Now()
		p.mu.Unlock()
		leaseEnded := LifecyclePhase(ctx, "service.lease", "service", reflect.TypeFor[T]().String(), "device_id", udid, "connection_generation", e.generation)
		cancelFinished := make(chan struct{})
		stopCancel := context.AfterFunc(ctx, func() {
			defer close(cancelFinished)
			ended := LifecyclePhase(ctx, "service.cancel_close", "service", reflect.TypeFor[T]().String(), "device_id", udid, "connection_generation", e.generation)
			p.retire(udid, e)
			ended(ctx.Err())
		})
		var once sync.Once
		release := func() {
			once.Do(func() {
				// Join only our lightweight close callback, never an RPC handler.
				// It must finish before another caller can own this entry.
				if !stopCancel() {
					<-cancelFinished
				}
				p.mu.Lock()
				e.busy = false
				e.lastUsed = time.Now()
				p.mu.Unlock()
				e.permit <- struct{}{}
				leaseEnded(ctx.Err())
			})
		}
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return &ServiceLease[T]{Conn: e.conn, Release: release, Invalidate: func() { p.retire(udid, e) }}, nil
	}
}

func (p *ServicePool[T]) retire(udid string, e *poolEntry[T]) {
	p.mu.Lock()
	e.retired = true
	if p.entries[udid] == e {
		delete(p.entries, udid)
	}
	p.mu.Unlock()
	e.closeOnce.Do(func() { _ = p.closeFn(e.conn) })
}

// Invalidate drops the cached connection for udid (closing it). Call
// after a transport-level error so the next Acquire re-handshakes.
func (p *ServicePool[T]) Invalidate(udid string) {
	p.mu.Lock()
	e := p.entries[udid]
	if e != nil {
		e.retired = true
		delete(p.entries, udid)
	}
	p.mu.Unlock()
	if e != nil {
		e.closeOnce.Do(func() { _ = p.closeFn(e.conn) })
	}
}

// Close drains every cached connection and stops the sweeper.
func (p *ServicePool[T]) Close() error {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.stopped = true
	close(p.stop)
	entries := p.entries
	p.entries = map[string]*poolEntry[T]{}
	for _, e := range entries {
		e.retired = true
	}
	p.mu.Unlock()
	for _, e := range entries {
		e.closeOnce.Do(func() { _ = p.closeFn(e.conn) })
	}
	return nil
}

// Opens returns the number of underlying service-channel handshakes
// the pool has performed across its lifetime. Useful in tests to
// confirm the pool is amortising opens — for N operations on the same
// UDID with no Invalidate calls, Opens should be exactly 1.
func (p *ServicePool[T]) Opens() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opens
}

// sweep runs every idleTTL/2 and evicts entries idle past idleTTL.
func (p *ServicePool[T]) sweep() {
	interval := p.idleTTL / 2
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case now := <-t.C:
			p.mu.Lock()
			var stale []*poolEntry[T]
			for udid, e := range p.entries {
				if !e.busy && now.Sub(e.lastUsed) >= p.idleTTL {
					e.retired = true
					stale = append(stale, e)
					delete(p.entries, udid)
				}
			}
			p.mu.Unlock()
			for _, e := range stale {
				e.closeOnce.Do(func() { _ = p.closeFn(e.conn) })
			}
		}
	}
}

type poolError string

func (e poolError) Error() string { return string(e) }

const errPoolClosed poolError = "goios: pool is closed"
