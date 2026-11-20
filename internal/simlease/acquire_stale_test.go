// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"context"
	"sync"
	"testing"
)

// afterListSimctl runs a hook once, after the next List snapshot is
// taken and before it is returned, so the caller's view is stale.
type afterListSimctl struct {
	*fakeSimctl
	mu        sync.Mutex
	afterList func()
}

func (a *afterListSimctl) List(ctx context.Context) ([]Sim, error) {
	sims, err := a.fakeSimctl.List(ctx)
	a.mu.Lock()
	hook := a.afterList
	a.afterList = nil
	a.mu.Unlock()
	if hook != nil {
		hook()
	}
	return sims, err
}

// Acquire chooses from a list taken before it holds the book. A release
// that finishes in between shuts the chosen simulator down (or deletes
// it), and Acquire used to lease it as already running (DH-34).
func TestAcquireNeverLeasesAStaleRunningSimulator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		created bool // a created lease's release deletes the simulator
	}{
		{name: "shut down by release"},
		{name: "deleted by release", created: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSimctl{}
			if !tc.created {
				f.add("AAAAAAAA-0000-4000-8000-000000000001", "shut", iphone16, runtime18, "Shutdown")
			}
			s := &afterListSimctl{fakeSimctl: f}
			m := newMemoryManager(t, s)
			a, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
			if err != nil {
				t.Fatal(err)
			}
			if f.state(a.UDID) != "Booted" {
				t.Fatalf("setup: a's simulator is %q", f.state(a.UDID))
			}
			s.mu.Lock()
			s.afterList = func() {
				if _, err := m.Release(context.Background(), a.ID, "a"); err != nil {
					t.Errorf("release: %v", err)
				}
			}
			s.mu.Unlock()
			b, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "b"})
			if err != nil {
				if n := len(m.Status(ctx).Leases); n != 0 {
					t.Fatalf("refused acquire left %d leases on the book", n)
				}
				return
			}
			if got := f.state(b.UDID); got != "Booted" {
				t.Fatalf("acquire returned lease %s (action %s) on %s, but that simulator is %q",
					b.ID, b.Action, b.UDID, got)
			}
			if b.Action == ActionAlreadyRunning {
				t.Fatalf("lease on a simulator spyder booted is marked %s, so release would leave it running", b.Action)
			}
		})
	}
}

func newMemoryManager(t *testing.T, s Simctl) *Manager {
	t.Helper()
	m, err := New(&Args{Simctl: s})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
