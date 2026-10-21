// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import "sync"

// deviceInit is one mutex per UDID covering this process's RSD/XPC
// initializers. resolve holds it across the handshake. The runwda
// reinstantiation holds the same lock, so the two never overlap on one
// device (🎯T167, the overlap 🎯T161 reproduced).
func (r *Resolver) deviceInit(udid string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deviceInitMu == nil {
		r.deviceInitMu = map[string]*sync.Mutex{}
	}
	m := r.deviceInitMu[udid]
	if m == nil {
		m = &sync.Mutex{}
		r.deviceInitMu[udid] = m
	}
	return m
}

// WithDeviceInitializer runs fn while this process's RSD/XPC initializer
// lock for udid is held. A nil resolver or empty UDID runs fn unlocked.
func (r *Resolver) WithDeviceInitializer(udid string, fn func() error) error {
	if fn == nil {
		return nil
	}
	if r == nil || udid == "" {
		return fn()
	}
	m := r.deviceInit(udid)
	m.Lock()
	defer m.Unlock()
	return fn()
}

// TryDeviceInitializer acquires the same lock as WithDeviceInitializer
// without waiting. ok is false when another initializer in this process
// already holds it. The returned function releases the lock; it is nil
// when ok is false.
func (r *Resolver) TryDeviceInitializer(udid string) (func(), bool) {
	if r == nil || udid == "" {
		return func() {}, true
	}
	m := r.deviceInit(udid)
	if !m.TryLock() {
		return nil, false
	}
	return m.Unlock, true
}
