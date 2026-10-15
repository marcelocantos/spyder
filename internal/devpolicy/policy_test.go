// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devpolicy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScopeResolutionAndProvenance(t *testing.T) {
	c := Empty()
	c.Global = Flags{"orientation": "global", "global": "yes"}
	c.Groups = []Group{{Name: "minicades", BundleIDs: []string{"gokarting.minicadesmobile.com"}, BundlePrefixes: []string{"com.minicades."}, Flags: Flags{"orientation": "group"}}}
	c.Apps["com.minicades.stockcars"] = Flags{"orientation": "app"}
	for _, tt := range []struct{ id, source, value string }{
		{"com.other.game", "global", "global"}, {"com.minicades.karts", "group:minicades", "group"}, {"gokarting.minicadesmobile.com", "group:minicades", "group"}, {"com.minicades.stockcars", "app:com.minicades.stockcars", "app"}, {"com.minicadesfake.game", "global", "global"},
	} {
		t.Run(tt.id, func(t *testing.T) {
			r := c.Resolve(tt.id, nil)
			if r.Flags["orientation"] != tt.value || r.Provenance["orientation"] != tt.source {
				t.Fatal(r)
			}
			if r.Flags["global"] != "yes" {
				t.Fatal(r)
			}
		})
	}
	r := c.Resolve("com.minicades.stockcars", Flags{"orientation": "session"})
	if r.Provenance["orientation"] != "session" {
		t.Fatal(r)
	}
}

func TestStoreValidationDoesNotPartiallyWrite(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "dev-policy.json")}
	c := Empty()
	c.Global = Flags{"orientation": "landscapeRight"}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.Path)
	bad := c
	bad.Groups = []Group{{Name: "bad", BundlePrefixes: []string{"com.minicades"}}}
	if err := s.Save(bad); err == nil {
		t.Fatal("accepted unsafe prefix")
	}
	after, _ := os.ReadFile(s.Path)
	if string(before) != string(after) {
		t.Fatal("invalid configuration replaced policy")
	}
	loaded, err := s.Load()
	if err != nil || loaded.Global["orientation"] != "landscapeRight" {
		t.Fatal(loaded, err)
	}
	if err = os.WriteFile(s.Path, []byte(`{"version":1,"typo":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(); err == nil {
		t.Fatal("accepted unknown field")
	}
}

func TestLeasesApplyRenewRevokeReconnectAndReservations(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := Empty()
	cfg.Owner = "office"
	cfg.Global = Flags{"orientation": "landscapeRight"}
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	peers := []Peer{{ID: "session1", DeviceID: "usb", BundleID: "com.minicades.stockcars", Capable: true}, {ID: "legacy", DeviceID: "usb", BundleID: "legacy"}}
	physical := true
	var proofErr, errorCall, errorReservation error
	var calls []Flags
	now := time.Unix(1000, 0)
	c := Controller{Store: s, Now: func() time.Time { return now }, Peers: func() []Peer { return peers }, USB: func(context.Context) (map[string]bool, error) { return map[string]bool{"usb": physical}, proofErr }, Authorize: func(_, owner string) error {
		if owner != "office" {
			t.Fatal(owner)
		}
		return errorReservation
	}, Call: func(_ context.Context, p Peer, f Flags, lease time.Duration) (any, error) {
		if p.ID == "legacy" {
			t.Fatal("called incapable app")
		}
		if lease != Lease {
			t.Fatal(lease)
		}
		calls = append(calls, maps.Clone(f))
		return map[string]any{"applied": true}, errorCall
	}}
	sweep := func() { t.Helper(); c.Reconcile(context.Background()); now = now.Add(PollInterval) }
	sweep()
	sweep()
	if len(calls) != 2 || calls[1]["orientation"] != "landscapeRight" {
		t.Fatal(calls)
	}
	physical = false
	sweep()
	if len(calls[2]) != 0 {
		t.Fatal("did not revoke after USB removal", calls)
	}
	sweep()
	if len(calls) != 3 {
		t.Fatal("repeated empty revoke", calls)
	}
	physical = true
	sweep()
	if calls[3]["orientation"] != "landscapeRight" {
		t.Fatal("did not reapply after reattach", calls)
	}
	proofErr = errors.New("enumeration failed")
	sweep()
	if len(calls[4]) != 0 {
		t.Fatal("renewed without fresh proof")
	}
	for _, status := range c.Statuses() {
		if status.SessionID == "session1" && (status.Error == "" || len(status.Applied) != 0) {
			t.Fatal(status)
		}
	}
	proofErr = nil
	errorReservation = errors.New("reserved by somebody else")
	sweep()
	if len(calls[len(calls)-1]) != 0 {
		t.Fatal("overrode reservation")
	}
	errorReservation = nil
	errorCall = errors.New("timeout")
	sweep()
	for _, status := range c.Statuses() {
		if status.SessionID == "session1" && (status.Error == "" || len(status.Applied) != 0) {
			t.Fatal("false applied claim", status)
		}
	}
	errorCall = nil
	peers[0].ID = "reconnected"
	sweep()
	if calls[len(calls)-1]["orientation"] != "landscapeRight" {
		t.Fatal("did not configure reconnected session")
	}
	peers = nil
	sweep()
	if len(c.Statuses()) != 0 {
		t.Fatal("retained dead session")
	}
}

func TestUnknownUSBAndCorruptPolicyNeverApply(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	if err := os.WriteFile(s.Path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	c := Controller{Store: s, Peers: func() []Peer { return []Peer{{ID: "1", DeviceID: "wifi", Capable: true}} }, USB: func(context.Context) (map[string]bool, error) {
		return map[string]bool{"wifi": true}, errors.New("unknown")
	}, Call: func(_ context.Context, _ Peer, f Flags, _ time.Duration) (any, error) {
		if len(f) != 0 {
			t.Fatal("applied flags without trustworthy configuration/proof")
		}
		return true, nil
	}}
	c.Reconcile(context.Background())
	if c.Statuses()[0].Error == "" {
		t.Fatal("hid unknown proof")
	}
}

func TestFailedRevokeExpiresAppliedStatus(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := Empty()
	cfg.Global = Flags{"orientation": "landscapeRight"}
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2000, 0)
	physical := true
	reachable := true
	c := Controller{Store: s, Now: func() time.Time { return now }, Peers: func() []Peer { return []Peer{{ID: "1", DeviceID: "USB", BundleID: "app", Capable: true}} }, USB: func(context.Context) (map[string]bool, error) { return map[string]bool{"USB": physical}, nil }, Call: func(context.Context, Peer, Flags, time.Duration) (any, error) {
		if !reachable {
			return nil, errors.New("channel lost")
		}
		return true, nil
	}}
	c.Reconcile(context.Background())
	if len(c.Statuses()[0].Applied) == 0 {
		t.Fatal("did not apply")
	}
	physical = false
	reachable = false
	now = now.Add(PollInterval)
	c.Reconcile(context.Background())
	if c.Statuses()[0].Error == "" || len(c.Statuses()[0].Applied) == 0 {
		t.Fatal("hid outstanding lease", c.Statuses())
	}
	now = now.Add(Lease)
	c.Reconcile(context.Background())
	if len(c.Statuses()[0].Applied) != 0 {
		t.Fatal("reported expired lease as applied", c.Statuses())
	}
}

func TestSlowPeersDoNotDelayHealthyRenewals(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := Empty()
	cfg.Global = Flags{"orientation": "landscapeRight"}
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	const slowPeers = 12
	peers := make([]Peer, 0, slowPeers+1)
	for i := range slowPeers {
		peers = append(peers, Peer{ID: fmt.Sprintf("slow-%d", i), DeviceID: "USB", Capable: true})
	}
	peers = append(peers, Peer{ID: "healthy", DeviceID: "USB", Capable: true})
	healthy := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	c := Controller{Store: s, Peers: func() []Peer { return peers }, USB: func(context.Context) (map[string]bool, error) { return map[string]bool{"USB": true}, nil }, Call: func(ctx context.Context, p Peer, f Flags, _ time.Duration) (any, error) {
		if p.ID == "healthy" {
			if f["orientation"] != "landscapeRight" {
				return nil, errors.New("healthy peer did not receive flags")
			}
			healthy <- struct{}{}
			return true, nil
		}
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	go func() { c.Reconcile(context.Background()); close(done) }()
	select {
	case <-healthy:
	case <-time.After(CallTimeout):
		close(release)
		<-done
		t.Fatal("healthy renewal waited behind slow peers")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(CallTimeout):
		t.Fatal("reconcile did not finish after release")
	}
}

func TestStaleUSBObservationCannotRenewLease(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := Empty()
	cfg.Global = Flags{"orientation": "landscapeRight"}
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	var sent Flags
	c := Controller{Store: s, Peers: func() []Peer { return []Peer{{ID: "1", DeviceID: "USB", Capable: true}} }, USB: func(context.Context) (map[string]bool, error) {
		// Simulate an enumeration implementation that returns late despite its
		// deadline. Its apparently positive census is too old to authorize a lease.
		time.Sleep(MaxProofAge + 20*time.Millisecond)
		return map[string]bool{"USB": true}, nil
	}, Call: func(_ context.Context, _ Peer, f Flags, _ time.Duration) (any, error) {
		sent = maps.Clone(f)
		return true, nil
	}}
	c.Reconcile(context.Background())
	if len(sent) != 0 || c.Statuses()[0].USB || c.Statuses()[0].Error != "USB proof is stale" {
		t.Fatal("used stale USB proof", sent, c.Statuses())
	}
}
