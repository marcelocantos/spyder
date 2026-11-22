// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package gorepro holds the reproductions for
// docs/analysis/lease-protocol-2026-11-22.md. The directory name starts
// with "_", so `go test ./...` skips it; run it by path:
//
//	go test -count=1 -v ./docs/analysis/lease-protocol-2026-11-22/_gorepro/
//
// Each test asserts the defective behaviour, so a passing test means the
// defect is present. Nothing here lists devices or touches simctl: the
// simulator tests use an in-memory Simctl and every state file lives in
// t.TempDir().
package gorepro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/simlease"
)

const (
	udid        = "00008103-001122334455667A" // an uninventoried physical iPhone
	displayName = "Lab iPhone"                // its name as usbmux reports it
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(paths.DataDirEnv, dir)
	t.Setenv("HOME", t.TempDir())
	return dir
}

// census stands in for the connected-device listing that `spyder run`
// (main.go connectedDevices) and the daemon (resolveUninventoried) make
// for a display name.
func census(present bool) []inventory.Connected {
	if !present {
		return nil
	}
	return []inventory.Connected{{ID: udid, Name: displayName}}
}

// R3. `spyder run --device "Lab iPhone"` keys its lease by UDID when the
// phone is listed and by the literal name when it is not (a replug, a
// reboot, or a census that misses its 4 s budget). Two jobs then hold
// leases on one phone.
func TestR3_DisplayNameKeyDependsOnPresence(t *testing.T) {
	dir := isolate(t)
	book := filepath.Join(dir, "reservations.json")
	inv := inventory.New()

	// Job A starts while the phone is connected (main.go:441-454).
	keyA := func(ref string) string { return inv.ReservationKey(ref, census(true)) }
	a, _, err := reservations.OpenBook(book, keyA)
	if err != nil {
		t.Fatal(err)
	}
	ra, err := a.Acquire(displayName, "job-A", reservations.DefaultTTL, "spyder run: A")
	if err != nil {
		t.Fatal(err)
	}

	// Job B starts during a replug: the census does not list the phone.
	keyB := func(ref string) string { return inv.ReservationKey(ref, census(false)) }
	b, _, err := reservations.OpenBook(book, keyB)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := b.Acquire(displayName, "job-B", reservations.DefaultTTL, "spyder run: B")
	if err != nil {
		t.Fatalf("defect absent: B was refused: %v", err)
	}
	live, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("A holds %q, B holds %q; live book: %+v", ra.Device, rb.Device, live)
	if len(live) != 2 || ra.Owner == rb.Owner {
		t.Fatalf("expected two live leases on one phone, got %+v", live)
	}
}

// R4. The daemon keys every call afresh. A release by display name made
// while the phone is unplugged keys to the literal name, finds nothing,
// and reports success; the UDID lease stays until it expires (up to
// reservations.MaxTTL, 24 h, for an MCP reserve).
func TestR4_ReleaseWhileAbsentReportsSuccessAndStrands(t *testing.T) {
	dir := isolate(t)
	inv := inventory.New()
	present := true
	daemonKey := func(ref string) string { return inv.ReservationKey(ref, census(present)) }
	d, _, err := reservations.OpenBook(filepath.Join(dir, "reservations.json"), daemonKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Acquire(displayName, "agent-A", reservations.MaxTTL, "mcp reserve"); err != nil {
		t.Fatal(err)
	}
	present = false // the phone drops off USB
	if _, err := d.Renew(displayName, "agent-A", time.Hour); err == nil {
		t.Fatal("renew while absent unexpectedly succeeded")
	} else {
		t.Logf("renew while absent: %v", err)
	}
	if err := d.Release(displayName, "agent-A"); err != nil {
		t.Fatalf("defect absent: release reported %v", err)
	}
	present = true // the phone comes back
	_, err = d.Acquire(displayName, "agent-B", time.Hour, "mcp reserve")
	if !reservations.IsConflict(err) {
		t.Fatalf("expected the released lease to still block B, got %v", err)
	}
	t.Logf("after a successful release, B is refused: %v", err)
}

// R5. A lease that lapses while its holder is suspended (laptop sleep,
// SIGSTOP, a renewal that failed twice) is never recovered: Renew refuses
// an expired record, `spyder run` only prints the error (main.go:589-597)
// and keeps the job running, and the next job is granted the device.
func TestR5_LapsedLeaseIsNotRecoveredAndIsRegranted(t *testing.T) {
	dir := isolate(t)
	book := filepath.Join(dir, "reservations.json")
	var mu sync.Mutex
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	a, err := reservations.New(book, reservations.WithNow(clock))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(udid, "job-A", reservations.DefaultTTL, ""); err != nil {
		t.Fatal(err)
	}
	// A renewed at minute 20 and the lid closed for 45 minutes. The
	// ticker runs on the monotonic clock and fires at awake minute 40,
	// which is wall minute 85.
	advance(20 * time.Minute)
	if _, err := a.Renew(udid, "job-A", reservations.DefaultTTL); err != nil {
		t.Fatal(err)
	}
	advance(65 * time.Minute)
	b, err := reservations.New(book, reservations.WithNow(clock))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Acquire(udid, "job-B", reservations.DefaultTTL, ""); err != nil {
		t.Fatalf("defect absent: B refused: %v", err)
	}
	_, err = a.Renew(udid, "job-A", reservations.DefaultTTL)
	if err == nil {
		t.Fatal("A's late renewal succeeded")
	}
	t.Logf("A's renewal after B's grant: %v (spyder run prints this and carries on)", err)
}

// --- simulator leases ---------------------------------------------------

const (
	iphone16  = "com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro"
	runtime18 = "com.apple.CoreSimulator.SimRuntime.iOS-18-6"
)

type fakeSimctl struct {
	mu    sync.Mutex
	sims  []simlease.Sim
	calls []string
}

func (f *fakeSimctl) List(context.Context) ([]simlease.Sim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sims), nil
}
func (f *fakeSimctl) DeviceTypes(context.Context) ([]simlease.DeviceType, error) {
	return []simlease.DeviceType{{ID: iphone16, Name: "iPhone 16 Pro"}}, nil
}
func (f *fakeSimctl) Runtimes(context.Context) ([]simlease.Runtime, error) {
	return []simlease.Runtime{{ID: runtime18, Name: "iOS 18.6", Platform: "iOS", Version: "18.6", IsAvailable: true}}, nil
}
func (f *fakeSimctl) Create(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("create not expected")
}
func (f *fakeSimctl) set(udid, verb, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, verb+" "+udid)
	for i := range f.sims {
		if f.sims[i].UDID == udid {
			f.sims[i].State = state
			return nil
		}
	}
	return fmt.Errorf("no such device %s", udid)
}
func (f *fakeSimctl) Boot(_ context.Context, u string) error     { return f.set(u, "boot", "Booted") }
func (f *fakeSimctl) Shutdown(_ context.Context, u string) error { return f.set(u, "shutdown", "Shutdown") }
func (f *fakeSimctl) Delete(_ context.Context, u string) error   { return f.set(u, "delete", "Deleted") }
func (f *fakeSimctl) RuntimeImages(context.Context) ([]simlease.RuntimeImage, error) {
	return nil, nil
}
func (f *fakeSimctl) DeleteRuntime(context.Context, string) error { return nil }
func (f *fakeSimctl) state(u string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sims {
		if s.UDID == u {
			return s.State
		}
	}
	return "gone"
}

func shutdownSim(u, name string) simlease.Sim {
	return simlease.Sim{UDID: u, Name: name, DeviceType: iphone16, Runtime: runtime18, State: "Shutdown", IsAvailable: true}
}

// R6. Activity renews a simulator lease in memory and writes it at most
// every 30 s across all leases (simlease.go Touch, touchPersistEvery).
// Any reload of the book (another lease's persisting Touch, a Reap pass)
// replaces the in-memory expiry with the file's. With two busy leases,
// B wins every write slot, A's renewals never reach the file, and the
// reaper shuts A's simulator down while A uses it every second.
func TestR6_ActivityRenewalLostAndReaperShutsDownBusySimulator(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSimctl{sims: []simlease.Sim{shutdownSim("SIM-A", "a"), shutdownSim("SIM-B", "b")}}
	var mu sync.Mutex
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	m, err := simlease.New(&simlease.Args{Simctl: f, Path: filepath.Join(dir, "sim-leases.json"), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	la, err := m.Acquire(ctx, simlease.AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "job-A", Name: "a", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	lb, err := m.Acquire(ctx, simlease.AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "job-B", Name: "b", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// Both jobs drive their simulator every second for ten minutes; B's
	// call lands first in each second. The reaper runs every 30 s.
	for sec := 1; sec <= 600; sec++ {
		mu.Lock()
		now = now.Add(time.Second)
		mu.Unlock()
		m.Touch(lb.UDID)
		m.Touch(la.UDID)
		if sec%30 == 0 {
			for _, r := range m.Reap(ctx) {
				t.Logf("t=+%ds reaper released %s (%s, owner %s), undo=%s, while it was in use",
					sec, r.Lease.ID, r.Lease.UDID, r.Lease.Owner, r.Undo)
			}
		}
		if f.state(la.UDID) != "Booted" {
			t.Logf("A's simulator is %s at t=+%ds although A touched it every second", f.state(la.UDID), sec)
			return
		}
	}
	t.Fatal("defect absent: A's simulator stayed booted")
}

// R7. A pending simulator lease left by a dead acquirer is reaped only if
// its operation_pid is dead (simlease.go operationActive: kill(pid, 0)).
// When that PID has been reused by any live process, the record is
// never reaped, cannot be released by its owner ("still being
// acquired"), and keeps the simulator out of every other acquire.
func TestR7_PendingLeaseWithReusedPIDIsNeverReaped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sim-leases.json")
	f := &fakeSimctl{sims: []simlease.Sim{shutdownSim("SIM-A", "a")}}
	f.sims[0].State = "Booted" // the dead acquirer had booted it
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	reused := os.Getppid() // any live process that is not the acquirer
	rec := []map[string]any{{
		"id": "deadbeefdeadbeef", "udid": "SIM-A", "name": "a",
		"device_type": iphone16, "runtime": runtime18, "owner": "job-A",
		"action": "booted", "ttl_s": 1800, "acquired_at": t0, "renewed_at": t0,
		"expires_at": t0.Add(30 * time.Minute), "pending": true, "operation_pid": reused,
	}}
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return t0.Add(48 * time.Hour) } // two days later
	m, err := simlease.New(&simlease.Args{Simctl: f, Path: path, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Reap(context.Background()); len(got) != 0 {
		t.Fatalf("defect absent: reaped %+v", got)
	}
	_, err = m.Release(context.Background(), "deadbeefdeadbeef", "job-A")
	if err == nil || !strings.Contains(err.Error(), "still being acquired") {
		t.Fatalf("owner release: %v", err)
	}
	_, err = m.Acquire(context.Background(), simlease.AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "job-B", Name: "a"})
	if err == nil {
		t.Fatal("B was granted the stranded simulator")
	}
	t.Logf("48 h later: not reaped; owner release: %q; B: %v", "still being acquired", err)
}
