// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devpolicy

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHealthyStatusVisibleWhileSiblingCallIsPending(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := Empty()
	cfg.Global = Flags{"orientation": "landscapeRight"}
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	healthyCalled, slowCalled, slowReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release, done := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	c := Controller{Store: store,
		Peers: func() []Peer {
			return []Peer{{ID: "healthy", DeviceID: "USB", Capable: true}, {ID: "slow", DeviceID: "USB", Capable: true}}
		},
		USB: func(context.Context) (map[string]bool, error) { return map[string]bool{"USB": true}, nil },
		Call: func(ctx context.Context, peer Peer, _ Flags, _ time.Duration) (any, error) {
			if peer.ID == "healthy" {
				close(healthyCalled)
				return "fresh-healthy-ack", nil
			}
			close(slowCalled)
			defer close(slowReturned)
			select {
			case <-release:
				return "slow-ack", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	before := time.Now()
	go func() { c.Reconcile(context.Background()); close(done) }()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(MaxProofAge):
			t.Error("owned reconcile did not finish after unconditional sibling release")
		}
	})
	for _, called := range []chan struct{}{healthyCalled, slowCalled} {
		select {
		case <-called:
		case <-time.After(MaxProofAge):
			t.Fatal("both actual controller workers did not enter their callbacks")
		}
	}
	// Poll only the read surface. The sibling remains inside its real bounded
	// callback until release or its unchanged CallTimeout; neither is a pass.
	tick := time.NewTicker(CallTimeout / 100)
	defer tick.Stop()
	deadline := time.NewTimer(MaxProofAge)
	defer deadline.Stop()
	for {
		for _, status := range c.Statuses() {
			if status.SessionID != "healthy" || status.Ack != "fresh-healthy-ack" {
				continue
			}
			select {
			case <-slowReturned:
				t.Fatal("healthy status was hidden until the sibling callback finished")
			default:
			}
			if !status.USB || status.Error != "" || status.Applied["orientation"] != "landscapeRight" || status.CheckedAt.Before(before) || time.Since(status.CheckedAt) > MaxProofAge || status.LeaseExpiresAt.Sub(status.CheckedAt) != Lease {
				t.Fatalf("published status changed the existing proof/lease semantics: %+v", status)
			}
			return
		}
		select {
		case <-slowReturned:
			t.Fatal("healthy ACK was not visible before the sibling callback finished")
		case <-deadline.C:
			t.Fatal("no fresh healthy status became visible within the existing proof age")
		case <-tick.C:
		}
	}
}

func TestStatusRefreshKeepsPresentPriorACKAndDropsDepartedPeer(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := Empty()
	cfg.Global = Flags{"orientation": "landscapeRight"}
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	c := Controller{Store: store,
		Peers: func() []Peer { return []Peer{{ID: "present", DeviceID: "USB", Capable: true}} },
		USB:   func(context.Context) (map[string]bool, error) { return map[string]bool{"USB": true}, nil },
		Call: func(ctx context.Context, _ Peer, _ Flags, _ time.Duration) (any, error) {
			close(entered)
			select {
			case <-release:
				return "fresh-ack", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		statuses: map[string]Status{
			"present":  {SessionID: "present", Ack: "prior-ack", Applied: Flags{"orientation": "landscapeRight"}, LeaseExpiresAt: time.Now().Add(Lease)},
			"departed": {SessionID: "departed", Ack: "must-disappear"},
		},
	}
	go func() { c.Reconcile(context.Background()); close(done) }()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(MaxProofAge):
			t.Error("owned reconcile did not finish after release")
		}
	})
	select {
	case <-entered:
	case <-time.After(MaxProofAge):
		t.Fatal("present worker never entered its callback")
	}
	statuses := c.Statuses()
	if len(statuses) != 1 || statuses[0].SessionID != "present" || statuses[0].Ack != "prior-ack" {
		t.Fatalf("refresh hid the current prior ACK or retained a departed peer: %+v", statuses)
	}
	statuses[0].Applied["orientation"] = "caller-mutation"
	if c.Statuses()[0].Applied["orientation"] != "landscapeRight" {
		t.Fatal("status reader mutated controller-owned flag state")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(MaxProofAge):
		t.Fatal("refresh did not finish")
	}
	statuses = c.Statuses()
	if len(statuses) != 1 || statuses[0].Ack != "fresh-ack" {
		t.Fatalf("final status did not replace the prior ACK: %+v", statuses)
	}
}
