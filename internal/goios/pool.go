// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"sync"
	"time"

	"github.com/danielpaulus/go-ios/ios"
)

// poolEntry is one cached service connection. The conn is held open
// across operations; the per-entry mutex serialises operations on it
// (most go-ios service connections are not safe for concurrent use).
type poolEntry[T any] struct {
	conn     T
	mu       sync.Mutex
	lastUsed time.Time
}

// ServicePool caches one go-ios service connection per (UDID, service)
// for a configurable idle TTL. Reduces usbmuxd session churn by reusing
// open connections across operations — particularly important on
// macOS where rapid open/close of service connections to the same
// device appears to wedge usbmuxd's per-device session table (🎯T67).
//
// Concurrency: ServicePool is safe for concurrent use. Acquire blocks
// only on the per-entry mutex while another caller holds the same
// connection; different (UDID, service) tuples are independent.
//
// Generic over the connection type so each service (installationproxy,
// appservice, screenshotr, etc.) can have its own typed pool without
// reflection. The factory + closer functions handle the type-specific
// open/close plumbing.
type ServicePool[T any] struct {
	resolver *Resolver
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
	opens int64
}

// NewServicePool constructs a pool wrapping the per-device service
// open/close pair. idleTTL bounds how long an idle connection is kept
// alive; a sweeper goroutine evicts older entries every TTL/2.
func NewServicePool[T any](r *Resolver, newConn func(ios.DeviceEntry) (T, error), closeFn func(T) error, idleTTL time.Duration) *ServicePool[T] {
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
// per-entry mutex is held until release fires. The returned T is
// safe to use until release.
//
// On first use for a UDID (or after an Invalidate or TTL eviction),
// Acquire opens a new connection via the pool's newConn factory.
func (p *ServicePool[T]) Acquire(udid string) (conn T, release func(), err error) {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		var zero T
		return zero, func() {}, errPoolClosed
	}
	e, ok := p.entries[udid]
	if !ok {
		dev, derr := p.resolver.Session(udid)
		if derr != nil {
			p.mu.Unlock()
			var zero T
			return zero, func() {}, derr
		}
		c, cerr := p.newConn(dev)
		if cerr != nil {
			p.mu.Unlock()
			var zero T
			return zero, func() {}, cerr
		}
		p.opens++
		e = &poolEntry[T]{conn: c, lastUsed: time.Now()}
		p.entries[udid] = e
	}
	p.mu.Unlock()

	e.mu.Lock()
	e.lastUsed = time.Now()
	return e.conn, func() {
		e.lastUsed = time.Now()
		e.mu.Unlock()
	}, nil
}

// Invalidate drops the cached connection for udid (closing it). Call
// after a transport-level error so the next Acquire re-handshakes.
func (p *ServicePool[T]) Invalidate(udid string) {
	p.mu.Lock()
	e, ok := p.entries[udid]
	if ok {
		delete(p.entries, udid)
	}
	p.mu.Unlock()
	if ok {
		_ = p.closeFn(e.conn)
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
	p.mu.Unlock()
	for _, e := range entries {
		_ = p.closeFn(e.conn)
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
				if now.Sub(e.lastUsed) >= p.idleTTL {
					stale = append(stale, e)
					delete(p.entries, udid)
				}
			}
			p.mu.Unlock()
			for _, e := range stale {
				_ = p.closeFn(e.conn)
			}
		}
	}
}

type poolError string

func (e poolError) Error() string { return string(e) }

const errPoolClosed poolError = "goios: pool is closed"
