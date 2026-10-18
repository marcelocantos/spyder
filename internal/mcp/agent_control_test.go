// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/agent"
)

// The handler drives the shipped supervisor: a second authorized client
// sees the same runner, idle drops the app and the host lease, and a
// non-owner mutation is rejected.
func TestAgentHandlers_ReuseIdleAndOwnership(t *testing.T) {
	prev := postLaunchSessionWait
	postLaunchSessionWait = 20 * time.Millisecond
	t.Cleanup(func() { postLaunchSessionWait = prev })

	const dev = "00008110-AAAABBBBCCCCDDDD"
	var mu sync.Mutex
	var appLaunches, taps int
	obs := agent.Observation{
		Tethered: true, RunnerAlive: true, RunnerPID: 42, Authorized: true,
		Installed: true, InstallGen: "gen-1", AUTBundle: "com.example.game",
		AUTRunning: true, HostSession: true, LeaseHeld: true,
	}
	var launches, installs int
	ios := &stubAdapter{
		launchApp: func(string, string, map[string]string) error {
			appLaunches++
			return nil
		},
		terminateApp: func(string, string) error { return nil },
		appPID:       func(string, string) (int, error) { return 0, nil },
		injectTap: func(string, int, int) error {
			taps++
			return nil
		},
	}
	h, store := newHandlerWithReservations(t, ios, nil)
	h.agents = agent.NewSupervisor(&agent.SupervisorArgs{
		Probe: func(string) (agent.Observation, error) {
			mu.Lock()
			defer mu.Unlock()
			return obs, nil
		},
		OwnerOf: func(id string) (string, bool) {
			r, ok := store.Get(id)
			if !ok || r.Owner == "" {
				return "", false
			}
			return r.Owner, true
		},
		Effects: agent.Effects{
			Launch: func(string) (int, error) {
				mu.Lock()
				launches++
				obs.RunnerAlive = true
				obs.RunnerPID = 42
				obs.Authorized = true
				mu.Unlock()
				return 42, nil
			},
			Install: func(string) (string, error) {
				mu.Lock()
				installs++
				obs.Installed = true
				obs.InstallGen = "gen-1"
				mu.Unlock()
				return "gen-1", nil
			},
			StopAUT: func(_, bundle string) error {
				mu.Lock()
				obs.AUTRunning = false
				mu.Unlock()
				if bundle != "com.example.game" {
					t.Errorf("stopped %s", bundle)
				}
				return nil
			},
			DropHostSession: func(string) error {
				mu.Lock()
				obs.HostSession = false
				mu.Unlock()
				return nil
			},
			DropLease: func(string) error {
				mu.Lock()
				obs.LeaseHeld = false
				mu.Unlock()
				return nil
			},
		},
	})

	// No client has ensured yet: a free device still launches.
	free := dispatchJSON(t, h, "launch_app", map[string]any{
		"device": dev, "bundle_id": "com.example.game", "owner": "alice",
	})
	if free.IsError {
		t.Fatalf("unsupervised launch: %s", resultText(t, &free))
	}

	if _, err := store.Acquire(dev, "alice", time.Hour, "t163"); err != nil {
		t.Fatal(err)
	}
	first := dispatchJSONMap(t, h, "agent_ensure", map[string]any{"device": dev, "owner": "alice"})
	if first["ready"] != true || pidOf(t, first) != 42 || launches != 0 || installs != 0 {
		t.Fatalf("first ensure %+v launches=%d installs=%d", first, launches, installs)
	}
	// The reservation is gone, so authorize() would allow a stranger.
	// The supervised owner still rejects them.
	if err := store.Release(dev, "alice"); err != nil {
		t.Fatal(err)
	}
	denied := dispatchJSON(t, h, "launch_app", map[string]any{
		"device": dev, "bundle_id": "com.example.game", "owner": "alice",
	})
	if !denied.IsError || !strings.Contains(resultText(t, &denied), agent.ErrNotOwner.Error()) {
		t.Fatalf("non-owner launch: %+v", denied)
	}
	if appLaunches != 1 {
		t.Fatalf("non-owner launch ran the app: %d", appLaunches)
	}
	tap := dispatchJSON(t, h, "input_tap", map[string]any{
		"device": dev, "owner": "stranger", "x": 1.0, "y": 2.0,
	})
	if !tap.IsError || !strings.Contains(resultText(t, &tap), agent.ErrNotOwner.Error()) || taps != 0 {
		t.Fatalf("non-owner tap %+v taps=%d", tap, taps)
	}

	if _, err := store.Acquire(dev, "bob", time.Hour, "t163"); err != nil {
		t.Fatal(err)
	}
	second := dispatchJSONMap(t, h, "agent_ensure", map[string]any{"device": dev, "owner": "bob"})
	if pidOf(t, second) != 42 || second["ready"] != true || launches != 0 || installs != 0 {
		t.Fatalf("second ensure %+v launches=%d installs=%d", second, launches, installs)
	}
	owned := dispatchJSON(t, h, "launch_app", map[string]any{
		"device": dev, "bundle_id": "com.example.game", "owner": "bob",
	})
	if owned.IsError {
		t.Fatalf("owner launch: %s", resultText(t, &owned))
	}

	idle := dispatchJSONMap(t, h, "agent_idle", map[string]any{"device": dev, "owner": "bob"})
	if idle["aut_running"] != false || idle["lease_held"] != false || idle["host_session"] != false || idle["ready"] != false {
		t.Fatalf("idle %+v", idle)
	}
	if pidOf(t, idle) != 42 {
		t.Fatalf("idle restarted the runner: %+v", idle)
	}
	if cond, _ := idle["condition"].(string); cond != string(agent.CondIdle) {
		t.Fatalf("idle condition %v", idle["condition"])
	}

	down, err := h.agents.NoteTether(dev, false)
	if err != nil || down.Ready || down.Condition != agent.CondDisconnected {
		t.Fatalf("disconnect %+v err=%v", down, err)
	}
	up, err := h.agents.NoteTether(dev, true)
	if err != nil || !up.Ready || !up.Recovered || up.Condition != agent.CondReconnected {
		t.Fatalf("reconnect %+v err=%v", up, err)
	}
	obs.RunnerAlive = false
	obs.Authorized = false
	lie, err := h.agents.NoteTether(dev, true)
	if err != nil || lie.Ready || lie.Condition != agent.CondReconnected {
		t.Fatalf("dead reconnect %+v err=%v", lie, err)
	}
}

func TestAgentHandlers_ConsentIsNotReady(t *testing.T) {
	const dev = "00008110-DDDDEEEEFFFF0000"
	h, store := newHandlerWithReservations(t, &stubAdapter{}, nil)
	if _, err := store.Acquire(dev, "alice", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	var launches int
	h.agents = agent.NewSupervisor(&agent.SupervisorArgs{
		Probe: func(string) (agent.Observation, error) {
			return agent.Observation{
				Tethered: true, Installed: true, InstallGen: "gen-1",
				ConsentRequired: true, ConsentActionable: true, ConsentMessage: "Touch ID",
			}, nil
		},
		OwnerOf: func(id string) (string, bool) {
			r, ok := store.Get(id)
			if !ok {
				return "", false
			}
			return r.Owner, true
		},
		Effects: agent.Effects{
			Launch: func(string) (int, error) {
				launches++
				return 1, nil
			},
		},
	})
	for i := 1; i <= agent.MaxAttempts; i++ {
		snap := dispatchJSONMap(t, h, "agent_ensure", map[string]any{"device": dev, "owner": "alice"})
		if snap["ready"] != false || snap["condition"] != string(agent.CondConsentRequired) {
			t.Fatalf("attempt %d %+v", i, snap)
		}
		consent, _ := snap["consent"].(map[string]any)
		if consent == nil || consent["message"] != "Touch ID" {
			t.Fatalf("attempt %d consent %+v", i, snap["consent"])
		}
	}
	if launches != 0 {
		t.Fatalf("consent launched %d times", launches)
	}
	exhausted := dispatchJSONMap(t, h, "agent_ensure", map[string]any{"device": dev, "owner": "alice"})
	if exhausted["ready"] != false || exhausted["condition"] != string(agent.CondRetryExhausted) || exhausted["consent"] != nil {
		t.Fatalf("exhausted %+v", exhausted)
	}
	if launches != 0 {
		t.Fatalf("exhausted launched %d", launches)
	}
}

func pidOf(t *testing.T, snap map[string]any) int {
	t.Helper()
	runner, _ := snap["runner"].(map[string]any)
	pid, _ := runner["pid"].(float64)
	return int(pid)
}
