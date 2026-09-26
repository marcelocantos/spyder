// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import "sync"

// resourceKeyHuman is the daemon-wide lock for owner gates. At most one
// human_gate is in flight anywhere on the daemon.
const resourceKeyHuman = "human_gate"

const defaultMaxWorkers = 8

type resourcePool struct {
	mu      sync.Mutex
	cond    *sync.Cond
	held    map[string]string // key -> runID
	workers int
	max     int
}

func newPool(max int) *resourcePool {
	if max <= 0 {
		max = defaultMaxWorkers
	}
	p := &resourcePool{
		held: map[string]string{},
		max:  max,
	}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func resourceKeys(step Step) []string {
	var keys []string
	if step.Device != "" {
		keys = append(keys, "device:"+step.Device)
	}
	if step.Mutex != "" {
		keys = append(keys, "mutex:"+step.Mutex)
	}
	if step.Type == KindHumanGate {
		keys = append(keys, resourceKeyHuman)
	}
	return keys
}

// tryStart takes a worker slot and the step's keys, or returns false.
func (p *resourcePool) tryStart(runID string, keys []string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.workers >= p.max {
		return false
	}
	for _, k := range keys {
		if _, ok := p.held[k]; ok {
			return false
		}
	}
	for _, k := range keys {
		p.held[k] = runID
	}
	p.workers++
	return true
}

func (p *resourcePool) finish(runID string, keys []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range keys {
		if p.held[k] == runID {
			delete(p.held, k)
		}
	}
	p.workers--
	p.cond.Broadcast()
}

func (p *resourcePool) wait() {
	p.mu.Lock()
	p.cond.Wait()
	p.mu.Unlock()
}

func (p *resourcePool) broadcast() {
	p.mu.Lock()
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *resourcePool) holding(key string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.held[key]
	return id, ok
}
