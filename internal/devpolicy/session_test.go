// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devpolicy

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func sessionFixture(t *testing.T) (*Controller, *time.Time, *SessionOverride) {
	t.Helper()
	now := time.Unix(5000, 0)
	s := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := Empty()
	cfg.Owner = "office"
	cfg.Global = Flags{"orientation": "landscapeRight"}
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	c := &Controller{Store: s, Now: func() time.Time { return now }, Authorize: func(_, owner string) error {
		if owner != "run-owner" && owner != "office" {
			return errors.New("foreign reservation")
		}
		return nil
	}, Peers: func() []Peer {
		return []Peer{{ID: "s1", DeviceID: "D1", BundleID: "app", Capable: true, AdsCapable: true}}
	}, USB: func(context.Context) (map[string]bool, error) { return map[string]bool{"D1": true}, nil }}
	c.AuthorizeSession = c.Authorize
	return c, &now, &SessionOverride{Owner: "run-owner", RunToken: "run1", DeviceID: "D1", BundleID: "app", Flags: Flags{"ads": "suppressed"}}
}

func TestSessionOverrideExpiresToFreshBaseAndClipsLease(t *testing.T) {
	c, now, s := sessionFixture(t)
	var calls []Flags
	var leases []time.Duration
	c.Call = func(_ context.Context, _ Peer, f Flags, d time.Duration) (any, error) {
		calls = append(calls, maps.Clone(f))
		leases = append(leases, d)
		return true, nil
	}
	if err := c.SetSession(*s, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	c.Reconcile(context.Background())
	if calls[0]["ads"] != "suppressed" || calls[0]["orientation"] != "landscapeRight" || leases[0] != 3*time.Second {
		t.Fatal(calls, leases)
	}
	// Never restore a stale copy of the persistent orientation policy.
	cfg, err := c.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Global["orientation"] = "landscapeLeft"
	if err = c.Store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(3 * time.Second)
	c.Reconcile(context.Background())
	if calls[1]["orientation"] != "landscapeLeft" || len(calls[1]) != 1 || len(c.SessionOverrides()) != 0 {
		t.Fatal(calls, c.SessionOverrides())
	}
	// A daemon restart has no ephemeral policy to resurrect.
	other := Controller{Store: c.Store}
	if len(other.SessionOverrides()) != 0 {
		t.Fatal("persisted run override")
	}
}

func TestSessionOverrideValidationConflictAndClearAreAtomic(t *testing.T) {
	c, now, s := sessionFixture(t)
	if err := c.SetSession(*s, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	before := c.SessionOverrides()[0]
	for _, bad := range []SessionOverride{
		{Owner: s.Owner, RunToken: "other", DeviceID: s.DeviceID, BundleID: s.BundleID, Flags: s.Flags},
		{Owner: "foreign", RunToken: s.RunToken, DeviceID: s.DeviceID, BundleID: s.BundleID, Flags: s.Flags},
		{Owner: s.Owner, RunToken: s.RunToken, DeviceID: s.DeviceID, BundleID: s.BundleID, Flags: Flags{"ads": "yes"}},
		{Owner: s.Owner, RunToken: s.RunToken, DeviceID: s.DeviceID, BundleID: s.BundleID, Flags: Flags{"ads": "normal", "orientation": "automatic"}},
	} {
		if err := c.SetSession(bad, 10*time.Second); err == nil {
			t.Fatal("accepted invalid/conflicting override", bad)
		}
	}
	if got := c.SessionOverrides()[0]; got.ExpiresAt != before.ExpiresAt || got.Flags["ads"] != "suppressed" {
		t.Fatal("partially changed", got)
	}
	wrong := *s
	wrong.RunToken = "wrong"
	if c.ClearSession(wrong) == nil {
		t.Fatal("stale clear removed new run")
	}
	*now = now.Add(time.Second)
	s.Flags = Flags{"ads": "normal"}
	if err := c.SetSession(*s, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := c.SessionOverrides()[0]; got.Flags["ads"] != "normal" || !got.ExpiresAt.After(before.ExpiresAt) {
		t.Fatal("renewal failed", got)
	}
	if err := c.ClearSession(*s); err != nil {
		t.Fatal(err)
	}
	if err := c.ClearSession(*s); err != nil {
		t.Fatal("not idempotent", err)
	}
	if len(c.SessionOverrides()) != 0 {
		t.Fatal("not cleared")
	}
}

func TestSessionOverridesAreDeviceBundleAndTokenIsolated(t *testing.T) {
	c, _, s := sessionFixture(t)
	if err := c.SetSession(*s, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, key := range []sessionKey{{"D2", "app"}, {"D1", "other-app"}} {
		other := *s
		other.DeviceID = key.device
		other.BundleID = key.bundle
		other.RunToken = "run2"
		other.Flags = Flags{"ads": "normal"}
		if err := c.SetSession(other, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.SessionOverrides()) != 3 {
		t.Fatal(c.SessionOverrides())
	}
	copy := c.SessionOverrides()
	copy[0].Flags["ads"] = "mutated"
	for _, got := range c.SessionOverrides() {
		if got.Flags["ads"] == "mutated" {
			t.Fatal("caller mutated live policy")
		}
	}
	if err := c.ClearSession(*s); err != nil {
		t.Fatal(err)
	}
	if len(c.SessionOverrides()) != 2 {
		t.Fatal("cleared sibling")
	}
}

func TestSessionAdsUnsupportedDoesNotBreakV1Orientation(t *testing.T) {
	c, _, s := sessionFixture(t)
	c.Peers = func() []Peer { return []Peer{{ID: "legacy", DeviceID: "D1", BundleID: "app", Capable: true}} }
	if err := c.SetSession(*s, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	c.Call = func(_ context.Context, p Peer, f Flags, _ time.Duration) (any, error) {
		if p.AdsCapable || len(f) != 1 || f["orientation"] != "landscapeRight" {
			t.Fatal(p, f)
		}
		return true, nil
	}
	c.Reconcile(context.Background())
	got := c.Statuses()[0]
	if got.UnsupportedFlags["ads"] != "suppressed" || got.Applied["ads"] != "" || got.Applied["orientation"] != "landscapeRight" {
		t.Fatal(got)
	}
}

func TestSessionRevokeOnUSBOrOwnerLossAndRebindOnColdSession(t *testing.T) {
	for _, cause := range []string{"usb", "owner"} {
		t.Run(cause, func(t *testing.T) {
			c, _, s := sessionFixture(t)
			if err := c.SetSession(*s, 10*time.Second); err != nil {
				t.Fatal(err)
			}
			var last Flags
			c.Call = func(_ context.Context, _ Peer, f Flags, _ time.Duration) (any, error) {
				last = maps.Clone(f)
				return true, nil
			}
			c.Reconcile(context.Background())
			if last["ads"] != "suppressed" {
				t.Fatal(last)
			}
			if cause == "usb" {
				c.USB = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
			} else {
				c.AuthorizeSession = func(string, string) error { return errors.New("owner lost") }
			}
			c.Reconcile(context.Background())
			if len(last) != 0 {
				t.Fatal("renewed lost proof", last)
			}
		})
	}
	c, _, s := sessionFixture(t)
	if err := c.SetSession(*s, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	c.Peers = func() []Peer {
		return []Peer{{ID: "new-cold-session", DeviceID: "D1", BundleID: "app", Capable: true, AdsCapable: true}}
	}
	c.Call = func(_ context.Context, p Peer, f Flags, _ time.Duration) (any, error) {
		if p.ID != "new-cold-session" || f["ads"] != "suppressed" {
			t.Fatal(p, f)
		}
		return true, nil
	}
	c.Reconcile(context.Background())
}

func TestSessionClearCannotRaceAnOlderRenewal(t *testing.T) {
	c, _, s := sessionFixture(t)
	if err := c.SetSession(*s, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	c.Call = func(_ context.Context, _ Peer, _ Flags, _ time.Duration) (any, error) {
		once.Do(func() { close(entered); <-release })
		return true, nil
	}
	done := make(chan struct{})
	go func() { c.Reconcile(context.Background()); close(done) }()
	<-entered
	cleared := make(chan error, 1)
	go func() { cleared <- c.ClearSession(*s) }()
	close(release)
	<-done
	if err := <-cleared; err != nil {
		t.Fatal(err)
	}
	c.Reconcile(context.Background())
	if c.Statuses()[0].Applied["ads"] != "" {
		t.Fatal("revived cleared lease", c.Statuses())
	}
}
