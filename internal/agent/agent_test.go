// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"errors"
	"testing"
)

type harness struct {
	obs       Observation
	launches  int
	installs  int
	stops     []string
	drops     int
	leases    int
	nextPID   int
	launchErr error
	holder    string
	held      bool
	onProbe   func()
	resets    int
	bundleFor func(string) string
}

func (h *harness) supervisor() *Supervisor {
	if h.nextPID == 0 {
		h.nextPID = 100
	}
	return NewSupervisor(&SupervisorArgs{
		RunnerBundleFor: h.bundleFor,
		Probe: func(string) (Observation, error) {
			if h.onProbe != nil {
				h.onProbe()
			}
			return h.obs, nil
		},
		OwnerOf: func(string) (string, bool) {
			return h.holder, h.held
		},
		Effects: Effects{
			Launch: func(string) (int, error) {
				h.launches++
				if h.launchErr != nil {
					return 0, h.launchErr
				}
				h.nextPID++
				h.obs.RunnerAlive = true
				h.obs.RunnerPID = h.nextPID
				h.obs.Authorized = true
				h.obs.Installed = true
				return h.nextPID, nil
			},
			Install: func(string) (string, error) {
				h.installs++
				h.obs.Installed = true
				h.obs.InstallGen = "installed"
				return "installed", nil
			},
			StopAUT: func(_, bundle string) error {
				h.stops = append(h.stops, bundle)
				h.obs.AUTRunning = false
				return nil
			},
			DropHostSession: func(string) error {
				h.drops++
				h.obs.HostSession = false
				return nil
			},
			DropLease: func(string) error {
				h.leases++
				h.obs.LeaseHeld = false
				return nil
			},
			ResetConsent: func(string) {
				h.resets++
				h.obs.ConsentRequired = false
				h.obs.ConsentActionable = false
				h.obs.ConsentMessage = ""
			},
		},
	})
}

func healthyObs() Observation {
	return Observation{
		Tethered:    true,
		LockKnown:   true,
		RunnerPID:   42,
		RunnerAlive: true,
		Authorized:  true,
		Installed:   true,
		InstallGen:  "gen-1",
		AUTBundle:   "com.example.game",
	}
}

func TestSupervisor_SecondClientReusesHealthyRunner(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	s := h.supervisor()
	first, err := s.Ensure("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	h.holder = "bob"
	second, err := s.Ensure("phone", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Ready || !second.Ready {
		t.Fatalf("ready first=%v second=%v", first.Ready, second.Ready)
	}
	if first.Runner.PID != 42 || second.Runner.PID != first.Runner.PID || second.Runner.InstallGen != "gen-1" {
		t.Fatalf("runner identity changed: %+v then %+v", first.Runner, second.Runner)
	}
	if first.Relaunched || second.Relaunched || first.Reinstalled || second.Reinstalled {
		t.Fatalf("healthy runner was restarted or reinstalled: %+v %+v", first, second)
	}
	if h.launches != 0 || h.installs != 0 {
		t.Fatalf("launch=%d install=%d, want 0", h.launches, h.installs)
	}
}

func TestSupervisor_RelaunchOnlyWhenDeadOrUnauthorized(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	s := h.supervisor()
	if _, err := s.Ensure("phone", "alice"); err != nil {
		t.Fatal(err)
	}
	if h.launches != 0 {
		t.Fatal("healthy ensure launched")
	}

	h.obs.RunnerAlive = false
	h.obs.RunnerPID = 0
	dead, err := s.Ensure("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !dead.Relaunched || h.launches != 1 || h.installs != 0 {
		t.Fatalf("dead runner: relaunched=%v launches=%d installs=%d", dead.Relaunched, h.launches, h.installs)
	}
	if dead.Runner.PID == 42 || dead.Runner.PID == 0 {
		t.Fatalf("dead relaunch pid=%d", dead.Runner.PID)
	}

	h.obs.RunnerAlive = true
	h.obs.Authorized = false
	h.obs.RunnerPID = dead.Runner.PID
	unauth, err := s.Ensure("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !unauth.Relaunched || h.launches != 2 {
		t.Fatalf("unauthorized relaunch=%v launches=%d", unauth.Relaunched, h.launches)
	}

	h.obs = healthyObs()
	if _, err := s.Ensure("phone", "alice"); err != nil {
		t.Fatal(err)
	}
	if h.launches != 2 || h.installs != 0 {
		t.Fatalf("healthy again launched or installed: launches=%d installs=%d", h.launches, h.installs)
	}

	before := h.launches
	h.obs.Locked = true
	h.obs.RunnerAlive = false
	locked, err := s.Ensure("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	h.obs.Locked = false
	h.obs.Tethered = false
	down, err := s.Ensure("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	h.obs = healthyObs()
	h.obs.ConsentRequired = true
	h.obs.ConsentActionable = true
	h.obs.RunnerAlive = false
	consent, err := s.Ensure("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if h.launches != before {
		t.Fatalf("locked, disconnected, or consent relaunched: launches %d -> %d", before, h.launches)
	}
	if locked.Condition != CondLocked || down.Condition != CondDisconnected || consent.Condition != CondConsentRequired {
		t.Fatalf("conditions locked=%s down=%s consent=%s", locked.Condition, down.Condition, consent.Condition)
	}
	if locked.Ready || down.Ready || consent.Ready {
		t.Fatal("non-healthy condition reported ready")
	}
}

func TestSupervisor_IdleStopsAppAndDropsLeaseAndHostSession(t *testing.T) {
	obs := healthyObs()
	obs.AUTRunning = true
	obs.LeaseHeld = true
	obs.HostSession = true
	h := &harness{obs: obs, holder: "alice", held: true}
	s := h.supervisor()
	if _, err := s.Ensure("phone", "alice"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Idle("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if snap.AUTRunning || snap.LeaseHeld || snap.HostSession {
		t.Fatalf("idle still holds work: aut=%v lease=%v host=%v", snap.AUTRunning, snap.LeaseHeld, snap.HostSession)
	}
	if snap.Condition != CondIdle || snap.Ready {
		t.Fatalf("idle condition=%s ready=%v", snap.Condition, snap.Ready)
	}
	if snap.Runner.PID != 42 || !snap.RunnerHealthy {
		t.Fatalf("idle dropped the healthy runner: %+v healthy=%v", snap.Runner, snap.RunnerHealthy)
	}
	if len(h.stops) != 1 || h.stops[0] != "com.example.game" || h.drops != 1 || h.leases != 1 {
		t.Fatalf("effects stops=%v drops=%d leases=%d", h.stops, h.drops, h.leases)
	}
	if h.launches != 0 || h.installs != 0 {
		t.Fatal("idle restarted or reinstalled the runner")
	}
}

func TestSupervisor_MutationWithoutOwnerFails(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	s := h.supervisor()
	if _, err := s.Ensure("phone", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("phone", "bob", MutInput); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("foreign input: %v", err)
	}
	if err := s.Mutate("phone", "", MutLifecycle); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("empty lifecycle: %v", err)
	}
	h.held = false
	if err := s.Mutate("phone", "alice", MutLifecycle); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("no holder: %v", err)
	}
	h.held = true
	if err := s.Mutate("phone", "alice", MutInput); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("phone", "alice", MutLifecycle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ensure("phone", "bob"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner ensure: %v", err)
	}
}

func TestSupervisor_ConditionsAreBounded(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	s := h.supervisor()
	healthy := healthyObs()
	dead := healthyObs()
	dead.RunnerAlive = false
	dead.RunnerPID = 0
	dead.Authorized = false
	cases := []struct {
		cond    Condition
		obs     Observation
		ready   bool
		recover bool
	}{
		{CondStartup, Observation{}, false, false},
		{CondAuthorizationLost, dead, false, false},
		{CondLocked, Observation{Tethered: true, Locked: true}, false, false},
		{CondDisconnected, Observation{}, false, false},
		{CondReconnected, healthy, true, true},
		{CondReconnected, dead, false, false},
		{CondRebooted, healthy, false, false},
		{CondAgentFailure, dead, false, false},
		{CondConsentRequired, Observation{ConsentRequired: true, ConsentActionable: true, ConsentMessage: "Enable UI Automation"}, false, false},
	}
	for _, tc := range cases {
		snap := s.Apply("phone", tc.cond, tc.obs)
		if snap.Condition != tc.cond {
			t.Errorf("%s landed in %s", tc.cond, snap.Condition)
		}
		if snap.Ready != tc.ready || snap.Recovered != tc.recover {
			t.Errorf("%s ready=%v recovered=%v, want ready=%v recovered=%v", tc.cond, snap.Ready, snap.Recovered, tc.ready, tc.recover)
		}
	}
	reboot := s.Apply("phone", CondRebooted, healthy)
	if reboot.Runner.PID != 0 || reboot.Ready {
		t.Fatalf("reboot kept a runner or reported ready: %+v", reboot)
	}
	actionable := s.Apply("phone", CondConsentRequired, Observation{ConsentRequired: true, ConsentActionable: true, ConsentMessage: "Touch ID"})
	if actionable.Ready || actionable.Consent == nil || !actionable.Consent.Actionable || actionable.Consent.Message != "Touch ID" {
		t.Fatalf("actionable consent: %+v", actionable)
	}
	hidden := s.Apply("phone", CondConsentRequired, Observation{ConsentRequired: true, ConsentActionable: false, ConsentMessage: "later"})
	if hidden.Ready || hidden.Consent != nil {
		t.Fatalf("non-actionable consent was surfaced: %+v", hidden)
	}

	down, err := s.NoteTether("phone", false)
	if err != nil {
		t.Fatal(err)
	}
	if down.Condition != CondDisconnected || down.Ready {
		t.Fatalf("note down: %+v", down)
	}
	h.obs = healthy
	up, err := s.NoteTether("phone", true)
	if err != nil {
		t.Fatal(err)
	}
	if up.Condition != CondReconnected || !up.Ready || !up.Recovered {
		t.Fatalf("note up healthy: %+v", up)
	}
	h.obs.RunnerAlive = false
	h.obs.Authorized = false
	lie, err := s.NoteTether("pad", true)
	if err != nil {
		t.Fatal(err)
	}
	if lie.Ready || lie.Recovered || lie.Condition != CondReconnected {
		t.Fatalf("reconnect ready lie: %+v", lie)
	}
}

func TestSupervisor_ConsentIsNotReadyAndRetriesStop(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	h.obs.RunnerAlive = false
	h.obs.ConsentRequired = true
	h.obs.ConsentActionable = true
	h.obs.ConsentMessage = "Enable UI Automation"
	s := h.supervisor()
	// Seeing the same recorded consent failure again is not another
	// attempt (🎯T175): only a launch counts, and nothing is launched.
	for i := 1; i <= MaxAttempts+2; i++ {
		snap, err := s.Ensure("phone", "alice")
		if err != nil {
			t.Fatal(err)
		}
		if snap.Ready || snap.Condition != CondConsentRequired || snap.Consent == nil || snap.Attempts != 0 {
			t.Fatalf("observation %d: %+v", i, snap)
		}
	}
	if h.launches != 0 {
		t.Fatalf("consent launched %d times", h.launches)
	}

	h.launchErr = errors.New("runner died")
	h.obs = healthyObs()
	h.obs.RunnerAlive = false
	h.obs.ConsentRequired = false
	s = h.supervisor()
	for i := 0; i < MaxAttempts; i++ {
		snap, err := s.Ensure("phone", "alice")
		if err != nil {
			t.Fatal(err)
		}
		if snap.Ready {
			t.Fatalf("failed launch reported ready: %+v", snap)
		}
	}
	if h.launches != MaxAttempts {
		t.Fatalf("launches=%d want %d", h.launches, MaxAttempts)
	}
	again, err := s.Ensure("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if h.launches != MaxAttempts || again.Ready || again.Condition != CondRetryExhausted {
		t.Fatalf("retry did not stop: launches=%d snap=%+v", h.launches, again)
	}
}

func TestEnsure_ChangedBootIDStaysRebooted(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	h.obs.BootID = "boot-a"
	s := h.supervisor()
	first, err := s.Ensure("phone", "alice")
	if err != nil || !first.Ready || first.Condition != CondReady || first.BootID != "boot-a" || h.launches != 0 {
		t.Fatalf("first %+v launches=%d err=%v", first, h.launches, err)
	}
	h.obs.BootID = "boot-b"
	h.obs.RunnerAlive = true
	h.obs.RunnerPID = 42
	second, err := s.Ensure("phone", "alice")
	if err != nil || second.Condition != CondRebooted || second.Ready || second.Relaunched || second.BootID != "boot-b" || h.launches != 0 {
		t.Fatalf("changed boot %+v launches=%d err=%v", second, h.launches, err)
	}
	h.obs.BootID = ""
	third, err := s.Ensure("phone", "alice")
	if err != nil || !third.Ready || third.Condition != CondReady || third.BootID != "boot-b" || h.launches != 0 {
		t.Fatalf("empty boot id looked like a reboot: %+v launches=%d err=%v", third, h.launches, err)
	}
}

func TestEnsure_UnknownLockIsNotReady(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	h.obs.LockKnown = false
	h.obs.Locked = false
	s := h.supervisor()
	snap, err := s.Ensure("phone", "alice")
	if err != nil || snap.Ready || snap.Condition == CondReady || snap.Condition == CondLocked || h.launches != 0 {
		t.Fatalf("unknown lock %+v launches=%d err=%v", snap, h.launches, err)
	}
	if snap.Condition != CondAgentFailure {
		t.Fatalf("unknown lock condition %s", snap.Condition)
	}
}

func TestNoteTether_UnknownLockIsNotReady(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	s := h.supervisor()
	if _, err := s.Ensure("phone", "alice"); err != nil {
		t.Fatal(err)
	}
	h.obs.LockKnown = false
	h.obs.Locked = false
	up, err := s.NoteTether("phone", true)
	if err != nil || up.Ready || up.Recovered || up.Condition != CondReconnected {
		t.Fatalf("unknown lock reconnect %+v err=%v", up, err)
	}
}

func TestTick_RebootWhileIdleStaysRebooted(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	h.obs.BootID = "boot-a"
	s := h.supervisor()
	if _, err := s.Ensure("phone", "alice"); err != nil {
		t.Fatal(err)
	}
	idle, err := s.Idle("phone", "alice")
	if err != nil || idle.Condition != CondIdle {
		t.Fatalf("idle %+v err=%v", idle, err)
	}
	h.obs.BootID = "boot-b"
	launches := h.launches
	snap, err := s.Tick("phone")
	if err != nil || snap.Condition != CondRebooted || snap.Ready || snap.Relaunched || h.launches != launches {
		t.Fatalf("idle reboot %+v launches=%d err=%v", snap, h.launches, err)
	}
	again, err := s.Tick("phone")
	if err != nil || again.Relaunched || h.launches != launches {
		t.Fatalf("second tick relaunched %+v launches=%d err=%v", again, h.launches, err)
	}
}

func TestSupervisor_TickRestartsOnlyUnhealthy(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	s := h.supervisor()
	snap, err := s.Tick("phone")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Ready || snap.Relaunched || h.launches != 0 || snap.Runner.PID != 42 {
		t.Fatalf("tick healthy: %+v launches=%d", snap, h.launches)
	}
	h.obs.RunnerAlive = false
	h.obs.RunnerPID = 0
	snap, err = s.Tick("phone")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Relaunched || h.launches != 1 || !snap.Ready {
		t.Fatalf("tick dead: %+v launches=%d", snap, h.launches)
	}
}

func TestSupervisor_InstallsOnlyWhenMissing(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	h.obs.Installed = false
	h.obs.RunnerAlive = false
	h.obs.InstallGen = ""
	s := h.supervisor()
	snap, err := s.Ensure("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Reinstalled || h.installs != 1 || h.launches != 1 {
		t.Fatalf("missing bundle: reinstalled=%v installs=%d launches=%d", snap.Reinstalled, h.installs, h.launches)
	}
	if _, err := s.Ensure("phone", "alice"); err != nil {
		t.Fatal(err)
	}
	if h.installs != 1 || h.launches != 1 {
		t.Fatalf("second ensure reinstalled or relaunched: installs=%d launches=%d", h.installs, h.launches)
	}
}

// A reservation released while the probe ran must not start or stop
// anything for the previous holder (🎯T175, from d8ed75e3).
func TestSupervisor_OwnerLostDuringProbeCannotStartOrStop(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	h.obs.RunnerAlive = false
	h.obs.AUTRunning = true
	s := h.supervisor()
	h.onProbe = func() { h.held = false }
	if _, err := s.Ensure("phone", "alice"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("ensure after owner lost: %v", err)
	}
	h.held = true
	if _, err := s.Idle("phone", "alice"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("idle after owner lost: %v", err)
	}
	if h.launches != 0 || len(h.stops) != 0 || h.drops != 0 || h.leases != 0 {
		t.Fatalf("effects ran: launches=%d stops=%v drops=%d leases=%d", h.launches, h.stops, h.drops, h.leases)
	}
}

// Retry is the attended way out of RetryExhausted. It still needs the
// reservation, and plain Ensure keeps the cap.
func TestSupervisor_RetryStartsFreshBudget(t *testing.T) {
	h := &harness{obs: healthyObs(), holder: "alice", held: true}
	h.obs.RunnerAlive = false
	h.launchErr = errors.New("runner died")
	s := h.supervisor()
	for i := 0; i < MaxAttempts+1; i++ {
		if _, err := s.Ensure("phone", "alice"); err != nil {
			t.Fatal(err)
		}
	}
	if h.launches != MaxAttempts {
		t.Fatalf("launches=%d want %d", h.launches, MaxAttempts)
	}
	if _, err := s.Retry("phone", "bob"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner retry: %v", err)
	}
	if h.resets != 0 || h.launches != MaxAttempts {
		t.Fatalf("non-owner retry reset=%d launches=%d", h.resets, h.launches)
	}
	h.launchErr = nil
	snap, err := s.Retry("phone", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Ready || !snap.Relaunched || h.resets != 1 || h.launches != MaxAttempts+1 {
		t.Fatalf("retry %+v resets=%d launches=%d", snap, h.resets, h.launches)
	}
}

// A consent failure is not retried unattended until the device reboots.
// The new boot gets its own budget, so the daemon asks again by itself.
func TestTick_RebootGivesFreshBudget(t *testing.T) {
	h := &harness{obs: healthyObs()}
	h.obs.BootID = "boot-a"
	h.obs.RunnerAlive = false
	h.launchErr = ErrConsent
	s := h.supervisor()
	for i := 0; i < MaxAttempts+2; i++ {
		if _, err := s.Tick("phone"); err != nil {
			t.Fatal(err)
		}
		// The host remembers the consent failure until reset.
		h.obs.ConsentRequired = true
		h.obs.ConsentActionable = true
	}
	if h.launches != 1 {
		t.Fatalf("unattended consent relaunched: launches=%d", h.launches)
	}
	snap, _ := s.Snapshot("phone")
	if snap.Condition != CondConsentRequired || snap.Attempts != 1 {
		t.Fatalf("condition %s attempts %d", snap.Condition, snap.Attempts)
	}
	h.obs.BootID = "boot-b"
	h.launchErr = nil
	reboot, err := s.Tick("phone")
	if err != nil || reboot.Condition != CondRebooted || reboot.Attempts != 0 || h.resets != 1 {
		t.Fatalf("reboot %+v err=%v resets=%d", reboot, err, h.resets)
	}
	up, err := s.Tick("phone")
	if err != nil || !up.Ready || !up.Relaunched || h.launches != 2 {
		t.Fatalf("after reboot %+v err=%v launches=%d", up, err, h.launches)
	}
}

// Tick relaunches a runner that exited with nobody attached: the daemon,
// not a client, keeps the agent resident.
func TestTick_RelaunchesExitedRunnerUnattended(t *testing.T) {
	h := &harness{obs: healthyObs()}
	s := h.supervisor()
	if snap, err := s.Tick("phone"); err != nil || !snap.Ready || h.launches != 0 {
		t.Fatalf("healthy tick %+v err=%v launches=%d", snap, err, h.launches)
	}
	h.obs.RunnerAlive = false
	h.obs.RunnerPID = 0
	snap, err := s.Tick("phone")
	if err != nil || !snap.Ready || !snap.Relaunched || h.launches != 1 {
		t.Fatalf("exited tick %+v err=%v launches=%d", snap, err, h.launches)
	}
}

func TestSupervisor_RunnerBundlePerDevice(t *testing.T) {
	h := &harness{obs: healthyObs(), bundleFor: func(id string) string {
		if id == "dk" {
			return "com.example.devicekit.xctrunner"
		}
		return ""
	}}
	s := h.supervisor()
	dk, _ := s.Tick("dk")
	ar, _ := s.Tick("ar")
	if dk.Runner.BundleID != "com.example.devicekit.xctrunner" || ar.Runner.BundleID != RunnerBundle {
		t.Fatalf("bundles dk=%q ar=%q", dk.Runner.BundleID, ar.Runner.BundleID)
	}
}

// 🎯T175: a launch that is alive and waiting on approval counts as one
// attempt. Ticks over it neither relaunch nor count.
func TestTick_ConsentPendingIsOneAttempt(t *testing.T) {
	h := &harness{obs: healthyObs()}
	h.obs.RunnerAlive = false
	h.launchErr = ErrConsentPending
	s := h.supervisor()
	snap, err := s.Tick("phone")
	if err != nil || snap.Condition != CondConsentPending || !snap.ConsentPending || snap.Attempts != 1 || snap.Consent == nil || !snap.Relaunched {
		t.Fatalf("launch %+v err=%v", snap, err)
	}
	h.obs.ConsentPending = true
	h.obs.ConsentMessage = "approve on the device"
	h.obs.HostPID = 31213
	for i := 0; i < MaxAttempts+3; i++ {
		snap, err = s.Tick("phone")
		if err != nil || snap.Condition != CondConsentPending || snap.Attempts != 1 || snap.HostPID != 31213 || snap.Consent.Message != "approve on the device" {
			t.Fatalf("tick %d %+v err=%v", i, snap, err)
		}
	}
	if h.launches != 1 {
		t.Fatalf("pending launch relaunched: %d", h.launches)
	}
	// Approved: the runner answers on the same launch.
	h.obs.ConsentPending = false
	h.obs.ConsentMessage = ""
	h.obs.RunnerAlive = true
	h.obs.Authorized = true
	snap, err = s.Tick("phone")
	if err != nil || !snap.Ready || snap.Consent != nil || snap.ConsentPending || snap.Attempts != 0 || h.launches != 1 {
		t.Fatalf("approved %+v err=%v launches=%d", snap, err, h.launches)
	}
}

// A consent message is dropped as soon as nothing is asking any more,
// including when the device is merely unhealthy for another reason.
func TestSnapshot_ClearsStaleConsent(t *testing.T) {
	h := &harness{obs: healthyObs()}
	h.obs.RunnerAlive = false
	h.obs.ConsentRequired = true
	h.obs.ConsentActionable = true
	h.obs.ConsentMessage = "DeviceKit runner pid 6838 ..."
	s := h.supervisor()
	if snap, _ := s.Tick("phone"); snap.Consent == nil {
		t.Fatalf("consent not shown: %+v", snap)
	}
	h.obs.ConsentRequired = false
	h.obs.ConsentActionable = false
	h.obs.ConsentMessage = ""
	h.obs.LockKnown = false
	snap, _ := s.Tick("phone")
	if snap.Consent != nil {
		t.Fatalf("stale consent kept: %+v", snap)
	}
}

// LastExit from the host is kept across observations that carry none.
func TestSnapshot_LastExitSticks(t *testing.T) {
	h := &harness{obs: healthyObs()}
	h.obs.LastExit = "host pid 1: exit status 3"
	s := h.supervisor()
	s.Tick("phone")
	h.obs.LastExit = ""
	if snap, _ := s.Tick("phone"); snap.LastExit != "host pid 1: exit status 3" {
		t.Fatalf("last exit %+v", snap)
	}
}
