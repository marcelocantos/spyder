// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bootThenReadOnly makes the lease directory read-only once a boot has
// succeeded, so the write that finishes Acquire fails.
type bootThenReadOnly struct {
	*fakeSimctl
	dir string
}

func (b *bootThenReadOnly) Boot(ctx context.Context, udid string) error {
	if err := b.fakeSimctl.Boot(ctx, udid); err != nil {
		return err
	}
	return os.Chmod(b.dir, 0o555)
}

// A lease Acquire reports as granted must be on the book, or a restarted
// daemon never undoes its simulator; and one that never reached the book
// must not leave a simulator behind (DH-35).
func TestAcquireGrantsOnlyWhatTheBookRecords(t *testing.T) {
	t.Run("book unwritable before the claim", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sim-leases.json")
		// The lock file exists; only the book itself cannot be replaced.
		if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		f := &fakeSimctl{}
		m, err := New(&Args{Simctl: f, Path: path, Now: fixedClock()})
		if err != nil {
			t.Fatal(err)
		}
		l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
		if err == nil {
			t.Fatalf("Acquire granted %s (%s) although the book could not record it", l.ID, l.UDID)
		}
		if calls := f.mutations(); len(calls) != 0 {
			t.Fatalf("refused Acquire still touched simulators: %v", calls)
		}
	})
	t.Run("book unwritable after the boot", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sim-leases.json")
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		f := &fakeSimctl{}
		m, err := New(&Args{Simctl: &bootThenReadOnly{fakeSimctl: f, dir: dir}, Path: path, Now: fixedClock()})
		if err != nil {
			t.Fatal(err)
		}
		l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
		if err == nil {
			restarted, rerr := New(&Args{Simctl: f, Path: path, Now: fixedClock()})
			if rerr != nil {
				t.Fatal(rerr)
			}
			for _, x := range restarted.Status(ctx).Leases {
				if x.ID == l.ID && !x.Pending {
					return
				}
			}
			t.Fatalf("Acquire granted %s (%s) but a restarted daemon does not hold it", l.ID, l.UDID)
		}
		for _, s := range f.sims {
			if s.State != "Shutdown" {
				t.Fatalf("refused Acquire left simulator %s %s (calls %v)", s.UDID, s.State, f.mutations())
			}
		}
		if n := len(m.Status(ctx).Leases); n != 0 {
			t.Fatalf("refused Acquire left %d leases in memory", n)
		}
	})
}

func fixedClock() func() time.Time {
	t0 := time.Unix(1_800_000_000, 0)
	return func() time.Time { return t0 }
}
