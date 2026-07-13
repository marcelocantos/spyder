// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// startFactoryDouble stands up a test-double game-server factory (a fakeApp
// advertising spawn_instance whose handler forks an instance that dials back)
// and returns the manager + the factory's session.
func startFactoryDouble(t *testing.T) (*Manager, *Session) {
	t.Helper()
	m, l := startManagerAndListener(t)
	addr := fmt.Sprintf("127.0.0.1:%d", l.Port)
	factory := newFakeApp(t, addr, []string{MethodSpawnInstance})
	t.Cleanup(factory.close)

	var mu sync.Mutex
	var conns []net.Conn
	factory.on(MethodSpawnInstance, func(params []byte) (any, error) {
		var req SpawnRequest
		if err := UnpackParams(params, &req); err != nil {
			return nil, err
		}
		c := dialInstance(t, req.AppChannel, req.Game)
		mu.Lock()
		conns = append(conns, c)
		mu.Unlock()
		return map[string]any{"ok": true}, nil
	})
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return m, waitForSession(t, l)
}

// TestInstancePool_Acquire is the 🎯T92.1 clause-2 oracle: acquire-or-spawn,
// per-factory capacity cap, idle reuse, and linger→GC — all against the
// test-double factory, no ge dependency.
func TestInstancePool_Acquire(t *testing.T) {
	m, factory := startFactoryDouble(t)
	p := NewInstancePool(m, WithMaxPerFactory(2), WithLinger(60*time.Millisecond))
	ctx := context.Background()

	// First acquire spawns instance 1.
	i1, err := p.Acquire(ctx, factory, "tiltbuggy", "holderA", 5*time.Second)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	// Second acquire (i1 still held) spawns a distinct instance 2.
	i2, err := p.Acquire(ctx, factory, "tiltbuggy", "holderB", 5*time.Second)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if i1.ID == i2.ID {
		t.Fatal("expected a distinct instance while i1 is held")
	}
	// Third acquire exceeds the cap (max 2).
	if _, err := p.Acquire(ctx, factory, "tiltbuggy", "holderC", time.Second); err == nil {
		t.Fatal("expected a capacity error at max=2")
	}

	// Release i1 and immediately re-acquire → reuse the same instance, no spawn.
	p.Release(i1.ID)
	i1b, err := p.Acquire(ctx, factory, "tiltbuggy", "holderC", 5*time.Second)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if i1b.ID != i1.ID {
		t.Fatalf("expected reuse of %s, got a new instance %s", i1.ID, i1b.ID)
	}

	// Release both idle → linger→GC reaps them.
	p.Release(i1b.ID)
	p.Release(i2.ID)
	if !waitFor(2*time.Second, func() bool { return len(p.Instances()) == 0 }) {
		t.Fatalf("idle instances were not GC'd: %v", p.Instances())
	}
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
