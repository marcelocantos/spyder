// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

const (
	iphone16  = "com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro"
	ipadPro   = "com.apple.CoreSimulator.SimDeviceType.iPad-Pro-13-inch-M4-8GB"
	runtime18 = "com.apple.CoreSimulator.SimRuntime.iOS-18-6"
	runtime17 = "com.apple.CoreSimulator.SimRuntime.iOS-17-5"
)

// fakeSimctl is an in-memory simctl. It records every mutating call.
type fakeSimctl struct {
	mu        sync.Mutex
	sims      []Sim
	images    []RuntimeImage
	calls     []string
	next      int
	bootErr   error
	createErr error
}

func (f *fakeSimctl) RuntimeImages(context.Context) ([]RuntimeImage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.images), nil
}

func (f *fakeSimctl) DeleteRuntime(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "runtime-delete "+id)
	for i := range f.images {
		if f.images[i].ImageID == id {
			f.images = slices.Delete(f.images, i, i+1)
			return nil
		}
	}
	return fmt.Errorf("no such runtime %s", id)
}

func (f *fakeSimctl) add(udid, name, typ, rt, state string) {
	f.sims = append(f.sims, Sim{UDID: udid, Name: name, DeviceType: typ, Runtime: rt, State: state, IsAvailable: true})
}

func (f *fakeSimctl) state(udid string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sims {
		if s.UDID == udid {
			return s.State
		}
	}
	return "gone"
}

func (f *fakeSimctl) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeSimctl) List(context.Context) ([]Sim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sims), nil
}

func (f *fakeSimctl) DeviceTypes(context.Context) ([]DeviceType, error) {
	return []DeviceType{{ID: iphone16, Name: "iPhone 16 Pro"}, {ID: ipadPro, Name: "iPad Pro 13-inch (M4)"}}, nil
}

func (f *fakeSimctl) Runtimes(context.Context) ([]Runtime, error) {
	return []Runtime{
		{ID: runtime17, Name: "iOS 17.5", Platform: "iOS", Version: "17.5", IsAvailable: true},
		{ID: runtime18, Name: "iOS 18.6", Platform: "iOS", Version: "18.6", IsAvailable: true},
	}, nil
}

func (f *fakeSimctl) Create(_ context.Context, name, typ, rt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		f.calls = append(f.calls, "create-failed "+name)
		return "", f.createErr
	}
	f.next++
	udid := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.next)
	f.sims = append(f.sims, Sim{UDID: udid, Name: name, DeviceType: typ, Runtime: rt, State: "Shutdown", IsAvailable: true})
	f.calls = append(f.calls, "create "+name)
	return udid, nil
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

func (f *fakeSimctl) Boot(_ context.Context, udid string) error {
	if f.bootErr != nil {
		f.mu.Lock()
		f.calls = append(f.calls, "boot-failed "+udid)
		f.mu.Unlock()
		return f.bootErr
	}
	return f.set(udid, "boot", "Booted")
}

func (f *fakeSimctl) Shutdown(_ context.Context, udid string) error {
	return f.set(udid, "shutdown", "Shutdown")
}

func (f *fakeSimctl) Delete(_ context.Context, udid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete "+udid)
	for i := range f.sims {
		if f.sims[i].UDID == udid {
			if f.sims[i].State == "Booted" {
				return fmt.Errorf("unable to delete a device in the current state: Booted")
			}
			f.sims = slices.Delete(f.sims, i, i+1)
			return nil
		}
	}
	return fmt.Errorf("no such device %s", udid)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newManager(t *testing.T, f *fakeSimctl, c *clock, path string) *Manager {
	t.Helper()
	m, err := New(&Args{Simctl: f, Path: path, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var ctx = context.Background()

// Release undoes exactly what acquire did, for each of the three cases.
func TestReleaseUndoesWhatAcquireDid(t *testing.T) {
	cases := []struct {
		name       string
		existing   string // "" = none, else the existing sim's state
		wantAction Action
		wantUndo   string
		wantFinal  string // state of the leased sim after release
	}{
		{"created", "", ActionCreated, "deleted", "gone"},
		{"booted", "Shutdown", ActionBooted, "shut_down", "Shutdown"},
		{"already running", "Booted", ActionAlreadyRunning, "left_running", "Booted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSimctl{}
			if tc.existing != "" {
				f.add("AAAAAAAA-0000-4000-8000-000000000001", "mine", iphone16, runtime18, tc.existing)
			}
			c := &clock{t: time.Unix(1_800_000_000, 0)}
			m := newManager(t, f, c, "")

			l, err := m.Acquire(ctx, AcquireArgs{DeviceType: "iPhone 16 Pro", Runtime: "18.6", Owner: "agent-a"})
			if err != nil {
				t.Fatal(err)
			}
			if l.Action != tc.wantAction {
				t.Fatalf("action = %s, want %s", l.Action, tc.wantAction)
			}
			if got := f.state(l.UDID); got != "Booted" {
				t.Fatalf("after acquire state = %s, want Booted", got)
			}
			before := len(f.mutations())

			r, err := m.Release(ctx, l.ID, "agent-a")
			if err != nil {
				t.Fatal(err)
			}
			if r.Undo != tc.wantUndo {
				t.Fatalf("undo = %s, want %s", r.Undo, tc.wantUndo)
			}
			if got := f.state(l.UDID); got != tc.wantFinal {
				t.Fatalf("after release state = %s, want %s", got, tc.wantFinal)
			}
			if tc.wantAction == ActionAlreadyRunning && len(f.mutations()) != before {
				t.Fatalf("already-running release touched the simulator: %v", f.mutations()[before:])
			}
			if len(m.Status(ctx).Leases) != 0 {
				t.Fatal("lease still on the book after release")
			}
		})
	}
}

func TestReleaseRequiresOwner(t *testing.T) {
	f := &fakeSimctl{}
	m := newManager(t, f, &clock{t: time.Unix(1_800_000_000, 0)}, "")
	l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Release(ctx, l.UDID, "b"); err == nil {
		t.Fatal("release by another owner succeeded")
	}
	if f.state(l.UDID) != "Booted" {
		t.Fatal("foreign release changed the simulator")
	}
}

// A lapsed lease is reaped the same way release would undo it, and a
// lease file reloaded by a new Manager (a daemon restart) is reaped too.
func TestLapsedLeaseReapedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sim-leases.json")
	f := &fakeSimctl{}
	f.add("AAAAAAAA-0000-4000-8000-000000000001", "shut", iphone16, runtime17, "Shutdown")
	c := &clock{t: time.Unix(1_800_000_000, 0)}

	m1 := newManager(t, f, c, path)
	created, err := m1.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	booted, err := m1.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime17, Owner: "b", TTL: 20 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if created.Action != ActionCreated || booted.Action != ActionBooted {
		t.Fatalf("actions = %s, %s", created.Action, booted.Action)
	}

	// Not yet due.
	c.t = c.t.Add(4 * time.Minute)
	if got := m1.Reap(ctx); len(got) != 0 {
		t.Fatalf("reaped before expiry: %+v", got)
	}

	// Daemon restarts: a fresh Manager reads the file.
	c.t = c.t.Add(2 * time.Minute) // 6m: the 5m lease has lapsed
	m2 := newManager(t, f, c, path)
	got := m2.Reap(ctx)
	if len(got) != 1 || got[0].Lease.ID != created.ID || got[0].Undo != "deleted" || got[0].Reason != "expired" {
		t.Fatalf("reap after restart = %+v", got)
	}
	if f.state(created.UDID) != "gone" {
		t.Fatal("created simulator survived its expired lease")
	}
	if f.state(booted.UDID) != "Booted" {
		t.Fatal("unexpired lease was reaped")
	}

	// Activity renews: touching keeps the 20m lease alive past its first expiry.
	c.t = c.t.Add(10 * time.Minute) // 16m
	m2.Touch(booted.UDID)
	c.t = c.t.Add(10 * time.Minute) // 26m, 10m after the touch
	if got := m2.Reap(ctx); len(got) != 0 {
		t.Fatalf("touched lease reaped: %+v", got)
	}
	c.t = c.t.Add(11 * time.Minute) // 21m after the touch
	if got := m2.Reap(ctx); len(got) != 1 || got[0].Undo != "shut_down" {
		t.Fatalf("lapsed booted lease reap = %+v", got)
	}
	if f.state(booted.UDID) != "Shutdown" {
		t.Fatal("booted simulator still up after its lease lapsed")
	}

	m3 := newManager(t, f, c, path)
	if n := len(m3.Status(ctx).Leases); n != 0 {
		t.Fatalf("%d leases left on disk", n)
	}
}

// A booted simulator nobody leases is reported with its uptime and is
// never shut down by the reaper, however long it has been up.
func TestUnleasedBootedReportedAndSurvivesReaper(t *testing.T) {
	f := &fakeSimctl{}
	f.add("BBBBBBBB-0000-4000-8000-000000000001", "iPad Pro 13-inch (M4)", ipadPro, runtime18, "Booted")
	f.sims[0].Uptime = 4 * 24 * time.Hour
	f.add("CCCCCCCC-0000-4000-8000-000000000001", "spyder-pool-1234abcd", iphone16, runtime18, "Booted")
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	m := newManager(t, f, c, filepath.Join(t.TempDir(), "l.json"))

	l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if l.Action != ActionCreated {
		t.Fatalf("leased the pool's simulator: %+v", l)
	}

	st := m.Status(ctx)
	var ipad *Unleased
	for i := range st.Unleased {
		if st.Unleased[i].Name == "iPad Pro 13-inch (M4)" {
			ipad = &st.Unleased[i]
		}
	}
	if ipad == nil || ipad.UptimeSeconds != int64(4*24*time.Hour/time.Second) {
		t.Fatalf("unleased report = %+v", st.Unleased)
	}
	for _, u := range st.Unleased {
		if u.UDID == l.UDID {
			t.Fatal("leased simulator reported as unleased")
		}
	}

	c.t = c.t.Add(365 * 24 * time.Hour)
	m.Reap(ctx)
	if f.state("BBBBBBBB-0000-4000-8000-000000000001") != "Booted" ||
		f.state("CCCCCCCC-0000-4000-8000-000000000001") != "Booted" {
		t.Fatal("reaper shut down an unleased simulator")
	}
	for _, call := range f.mutations() {
		if call == "shutdown BBBBBBBB-0000-4000-8000-000000000001" {
			t.Fatal("reaper issued shutdown against an unleased simulator")
		}
	}
}

// A daemon that dies mid-acquire leaves a pending record; the next
// daemon undoes it even though its TTL has not run out.
func TestPendingLeaseFromDeadDaemonIsUndone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.json")
	f := &fakeSimctl{bootErr: errors.New("killed")}
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	m1 := newManager(t, f, c, path)

	// Simulate death between create and boot: write the pending record the
	// way Acquire does, then create the sim, then stop.
	m1.mu.Lock()
	l := &Lease{ID: "deadbeefdeadbeef", Name: NamePrefix + "deadbeef", DeviceType: iphone16, Runtime: runtime18,
		Owner: "a", Action: ActionCreated, Pending: true, TTLSeconds: 3600, AcquiredAt: c.t, ExpiresAt: c.t.Add(time.Hour)}
	m1.leases[l.ID] = l
	m1.mustPersistLocked()
	m1.mu.Unlock()
	udid, _ := f.Create(ctx, l.Name, iphone16, runtime18) // UDID never recorded

	m2 := newManager(t, f, c, path)
	got := m2.Reap(ctx)
	if len(got) != 1 || got[0].Undo != "deleted" {
		t.Fatalf("reap of abandoned pending lease = %+v", got)
	}
	if f.state(udid) != "gone" {
		t.Fatal("simulator from abandoned acquire survived")
	}
}

// A sim_acquire whose simctl create fails must not touch a simulator it
// did not create. The requested name is another owner's leased simulator
// on a different runtime; simctl's default names collide this way (DH-08).
func TestFailedCreateNeverDeletesAnotherSimulator(t *testing.T) {
	const victim = "AAAAAAAA-0000-4000-8000-0000000000AA"
	f := &fakeSimctl{}
	f.add(victim, "iPhone 16 Pro", iphone16, runtime18, "Booted")
	m := newManager(t, f, &clock{t: time.Unix(1_800_000_000, 0)}, "")

	b, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "b", Name: "iPhone 16 Pro"})
	if err != nil {
		t.Fatal(err)
	}
	if b.UDID != victim {
		t.Fatalf("setup: b leased %s, want %s", b.UDID, victim)
	}

	f.createErr = errors.New("simctl create: runtime unavailable")
	if _, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime17, Owner: "a", Name: "iPhone 16 Pro"}); err == nil {
		t.Fatal("acquire succeeded despite create failure")
	}
	if got := f.state(victim); got != "Booted" {
		t.Fatalf("owner b's leased simulator is %q after owner a's failed create (mutations: %v); want Booted",
			got, f.mutations())
	}
	if len(m.Status(ctx).Leases) != 1 || m.Status(ctx).Leases[0].ID != b.ID {
		t.Fatalf("leases after failed create = %+v", m.Status(ctx).Leases)
	}
}

// Create is only attempted when no eligible simulator matches. An
// unavailable simulator of the same name, type and runtime is not
// eligible, and a failed Create must not delete it: undo would see
// exactly one match (DH-08).
func TestFailedCreateLeavesSameNameSimulator(t *testing.T) {
	const hand = "AAAAAAAA-0000-4000-8000-0000000000AB"
	f := &fakeSimctl{createErr: errors.New("simctl create: runtime unavailable")}
	f.add(hand, "iPhone 16 Pro", iphone16, runtime17, "Shutdown")
	f.sims[0].IsAvailable = false
	m := newManager(t, f, &clock{t: time.Unix(1_800_000_000, 0)}, "")
	if _, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime17, Owner: "a", Name: "iPhone 16 Pro"}); err == nil {
		t.Fatal("acquire succeeded despite create failure")
	}
	if got := f.state(hand); got != "Shutdown" {
		t.Fatalf("unavailable simulator is %q (mutations: %v); want Shutdown", got, f.mutations())
	}
	if len(m.Status(ctx).Leases) != 0 {
		t.Fatalf("failed create left leases: %+v", m.Status(ctx).Leases)
	}
}

// A pending created record written the way the installed daemon writes
// it (no UDID, spyder-lease name, device type and runtime) is still
// undone, and a same-named simulator on another runtime is not.
func TestOldPendingLeaseStillUndoesOnlyItsSimulator(t *testing.T) {
	const (
		created = "00000000-0000-4000-8000-000000000099"
		other   = "AAAAAAAA-0000-4000-8000-0000000000AA"
	)
	path := filepath.Join(t.TempDir(), "l.json")
	body := `[
  {
    "id": "deadbeefdeadbeef",
    "udid": "",
    "name": "spyder-lease-deadbeef",
    "device_type": "com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro",
    "runtime": "com.apple.CoreSimulator.SimRuntime.iOS-18-6",
    "owner": "a",
    "action": "created",
    "ttl_s": 3600,
    "acquired_at": "2027-01-15T08:00:00Z",
    "renewed_at": "0001-01-01T00:00:00Z",
    "expires_at": "2027-01-15T09:00:00Z",
    "pending": true
  }
]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeSimctl{}
	f.add(created, "spyder-lease-deadbeef", iphone16, runtime18, "Shutdown")
	f.add(other, "spyder-lease-deadbeef", iphone16, runtime17, "Booted")
	m := newManager(t, f, &clock{t: time.Unix(1_800_000_000, 0)}, path)
	got := m.Reap(ctx)
	if len(got) != 1 || got[0].Undo != "deleted" {
		t.Fatalf("reap of old pending lease = %+v", got)
	}
	if f.state(created) != "gone" {
		t.Fatal("simulator named in the old pending record survived")
	}
	if f.state(other) != "Booted" {
		t.Fatalf("other runtime is %q; want Booted", f.state(other))
	}
}

// A pending created record with no UDID is crash recovery. Matching the
// name alone must not delete a simulator of another runtime, one another
// lease holds, one somebody claimed, or one of several name matches (DH-08).
func TestPendingCreateMatchesOnlyOneUnownedSimulator(t *testing.T) {
	const (
		otherRuntime = "AAAAAAAA-0000-4000-8000-0000000000AA"
		leased       = "BBBBBBBB-0000-4000-8000-0000000000BB"
		claimed      = "CCCCCCCC-0000-4000-8000-0000000000CC"
		ambiguousA   = "DDDDDDDD-0000-4000-8000-0000000000D1"
		ambiguousB   = "DDDDDDDD-0000-4000-8000-0000000000D2"
	)
	path := filepath.Join(t.TempDir(), "l.json")
	f := &fakeSimctl{}
	f.add(otherRuntime, "iPhone 16 Pro", iphone16, runtime18, "Booted")
	f.add(leased, "pinned", iphone16, runtime17, "Booted")
	f.add(claimed, "hand-made", iphone16, runtime17, "Booted")
	f.add(ambiguousA, "twin", iphone16, runtime17, "Shutdown")
	f.add(ambiguousB, "twin", iphone16, runtime17, "Shutdown")
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	m := newManager(t, f, c, path)
	if _, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime17, Owner: "b", Name: "pinned"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Claim(ClaimArgs{ID: claimed, Owner: "marcelo", Note: "hand-made"}); err != nil {
		t.Fatal(err)
	}

	pend := func(id, name, runtime string) {
		t.Helper()
		m.mu.Lock()
		m.leases[id] = &Lease{
			ID: id, Name: name, DeviceType: iphone16, Runtime: runtime,
			Owner: "a", Action: ActionCreated, Pending: true, TTLSeconds: 3600,
			AcquiredAt: c.t, ExpiresAt: c.t.Add(time.Hour),
		}
		m.mustPersistLocked()
		m.mu.Unlock()
	}
	pend("pending-other-runtime", "iPhone 16 Pro", runtime17)
	pend("pending-leased", "pinned", runtime17)
	pend("pending-claimed", "hand-made", runtime17)
	pend("pending-ambiguous", "twin", runtime17)

	m.Reap(ctx)
	for _, udid := range []string{otherRuntime, leased, claimed, ambiguousA, ambiguousB} {
		if f.state(udid) == "gone" {
			t.Fatalf("reaper deleted %s (mutations: %v)", udid, f.mutations())
		}
	}
}

func TestFailedBootUndoesCreate(t *testing.T) {
	f := &fakeSimctl{bootErr: errors.New("boot timed out")}
	m := newManager(t, f, &clock{t: time.Unix(1_800_000_000, 0)}, "")
	if _, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"}); err == nil {
		t.Fatal("acquire succeeded despite boot failure")
	}
	if len(f.sims) != 0 {
		t.Fatalf("failed acquire left simulators: %+v", f.sims)
	}
	if len(m.Status(ctx).Leases) != 0 {
		t.Fatal("failed acquire left a lease")
	}
}

func TestSameOwnerReacquireRenews(t *testing.T) {
	f := &fakeSimctl{}
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	m := newManager(t, f, c, "")
	a, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
	if err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(time.Minute)
	b, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || !b.ExpiresAt.After(a.ExpiresAt) {
		t.Fatalf("re-acquire did not renew: %+v then %+v", a, b)
	}
	other, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if other.UDID == a.UDID {
		t.Fatal("two owners lease one simulator")
	}
}

func TestEmptyRuntimeSelectsNewestIOS(t *testing.T) {
	m := newManager(t, &fakeSimctl{}, &clock{t: time.Unix(1_800_000_000, 0)}, "")
	rt, err := m.resolveRuntime(ctx, "")
	if err != nil || rt != runtime18 {
		t.Fatalf("runtime = %q, %v", rt, err)
	}
}

func TestParseLaunchdSimUptimes(t *testing.T) {
	ps := "   1-02:03:04 /Library/Developer/PrivateFrameworks/CoreSimulator.framework/Resources/bin/launchd_sim /Users/m/Library/Developer/CoreSimulator/Devices/ECE9E981-66E5-40C7-A2E2-B4340FFC73A1/data/var/run/launchd_bootstrap.plist\n" +
		"      05:06 /usr/bin/other /Users/m/Library/Developer/CoreSimulator/Devices/NOPE/data\n"
	got := parseLaunchdSimUptimes(ps)
	want := 26*time.Hour + 3*time.Minute + 4*time.Second
	if len(got) != 1 || got["ECE9E981-66E5-40C7-A2E2-B4340FFC73A1"] != want {
		t.Fatalf("uptimes = %v", got)
	}
}
