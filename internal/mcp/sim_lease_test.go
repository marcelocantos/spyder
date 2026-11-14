// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/simlease"
)

const (
	leaseTestType    = "com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro"
	leaseTestRuntime = "com.apple.CoreSimulator.SimRuntime.iOS-18-6"
	leaseTestUDID    = "5E1F0A2B-1111-4222-8333-444455556666"
)

// leaseSimctl is a one-simulator fake simctl.
type leaseSimctl struct {
	mu    sync.Mutex
	state string
}

func (f *leaseSimctl) List(context.Context) ([]simlease.Sim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []simlease.Sim{{UDID: leaseTestUDID, Name: "s", DeviceType: leaseTestType, Runtime: leaseTestRuntime, State: f.state, IsAvailable: true}}, nil
}
func (f *leaseSimctl) DeviceTypes(context.Context) ([]simlease.DeviceType, error) { return nil, nil }
func (f *leaseSimctl) Runtimes(context.Context) ([]simlease.Runtime, error)       { return nil, nil }
func (f *leaseSimctl) Create(context.Context, string, string, string) (string, error) {
	return "", errors.New("unexpected create")
}
func (f *leaseSimctl) Boot(context.Context, string) error {
	f.mu.Lock()
	f.state = "Booted"
	f.mu.Unlock()
	return nil
}
func (f *leaseSimctl) Shutdown(context.Context, string) error {
	f.mu.Lock()
	f.state = "Shutdown"
	f.mu.Unlock()
	return nil
}
func (f *leaseSimctl) Delete(context.Context, string) error { return errors.New("unexpected delete") }

// A leased simulator UDID is routed to the simctl adapter (not go-ios),
// using it renews the lease, and sim_release shuts it down again.
func TestLeasedSimulatorRoutesToSimctlAdapter(t *testing.T) {
	iosCalls := 0
	ios := &stubAdapter{launchApp: func(string, string, map[string]string) error { iosCalls++; return nil }}
	var simLaunched string
	sim := &stubAdapter{launchApp: func(id, bundle string, _ map[string]string) error { simLaunched = id + " " + bundle; return nil }}

	h := newHandlerWithStubs(t, ios, nil)
	h.sim = sim
	now := time.Unix(1_800_000_000, 0)
	f := &leaseSimctl{state: "Shutdown"}
	m, err := simlease.New(&simlease.Args{Simctl: f, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	h.SetSimLeases(m)
	h.bootedSims = func(context.Context) ([]device.Info, error) { return nil, nil } // hermetic

	res, err := h.Dispatch(context.Background(), "sim_acquire", map[string]any{
		"device_type": leaseTestType, "runtime": leaseTestRuntime, "owner": "agent", "ttl_s": float64(600),
	})
	if err != nil || res.IsError {
		t.Fatalf("sim_acquire: %v %+v", err, res)
	}
	var lease simlease.Lease
	if err := json.Unmarshal([]byte(leaseText(res)), &lease); err != nil {
		t.Fatal(err)
	}
	if lease.UDID != leaseTestUDID || lease.Action != simlease.ActionBooted {
		t.Fatalf("lease = %+v", lease)
	}

	now = now.Add(5 * time.Minute)
	res, err = h.Dispatch(context.Background(), "launch_app", map[string]any{
		"device": leaseTestUDID, "bundle_id": "com.example.app", "owner": "agent",
	})
	if err != nil || res.IsError {
		t.Fatalf("launch_app: %v %s", err, leaseText(res))
	}
	if simLaunched != leaseTestUDID+" com.example.app" || iosCalls != 0 {
		t.Fatalf("launch routed wrong: sim=%q ios calls=%d", simLaunched, iosCalls)
	}

	st := m.Status(context.Background())
	if len(st.Leases) != 1 || !st.Leases[0].ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("activity did not renew the lease: %+v", st.Leases)
	}

	res, err = h.Dispatch(context.Background(), "sim_release", map[string]any{"udid": leaseTestUDID, "owner": "agent"})
	if err != nil || res.IsError {
		t.Fatalf("sim_release: %v %s", err, leaseText(res))
	}
	if f.state != "Shutdown" {
		t.Fatalf("simulator state after release = %s", f.state)
	}
}

func leaseText(res *mcpgo.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(mcpgo.TextContent); ok {
		return tc.Text
	}
	return ""
}
