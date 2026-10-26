// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devpolicy

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// goios resolve is bounded at 15s (internal/goios/session.go). While one iOS
// session configure blocks that long, Android peers must keep renewing.
const iosResolveHold = 15 * time.Second

func TestAndroidLeasesRenewWhileIOSDispatchBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("full goios resolve hold")
	}
	s := &Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := Empty()
	cfg.Global = Flags{"orientation": "landscapeRight", "ads": "suppressed"}
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	iosStarted := make(chan struct{})
	var iosStartOnce sync.Once
	now := time.Unix(10_000, 0)
	androidPeer := Peer{ID: "android-1", DeviceID: "adb-serial", BundleID: "com.game", Capable: true, AdsCapable: true}
	iosPeer := Peer{ID: "ios-1", DeviceID: "iphone-udid", BundleID: "com.game", Capable: true}
	var androidRenewals atomic.Int32
	c := Controller{
		Store: s,
		Now:   func() time.Time { return now },
		Peers: func() []Peer { return []Peer{iosPeer, androidPeer} },
		USB: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"adb-serial": true, "iphone-udid": true}, nil
		},
		Call: func(_ context.Context, p Peer, f Flags, _ time.Duration) (any, error) {
			if p.ID == iosPeer.ID {
				iosStartOnce.Do(func() { close(iosStarted) })
				// Hold the peer dispatch slot for the full goios resolve budget even
				// though configure calls use a shorter deadline — the incident was
				// reconcile waiting on a stuck sibling, not a successful configure.
				timer := time.NewTimer(iosResolveHold)
				defer timer.Stop()
				<-timer.C
				return true, nil
			}
			if f["orientation"] != "landscapeRight" {
				t.Fatalf("android lost flags: %+v", f)
			}
			androidRenewals.Add(1)
			return true, nil
		},
	}
	ctx := context.Background()
	c.Reconcile(ctx)
	select {
	case <-iosStarted:
	case <-time.After(time.Second):
		t.Fatal("slow iOS worker never started")
	}
	waitAndroid := func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			for _, st := range c.Statuses() {
				if st.SessionID == androidPeer.ID && len(st.Applied) > 0 && now.Before(st.LeaseExpiresAt) {
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("android lease not renewed for simulated now=%s: %+v", now, c.Statuses())
	}
	waitAndroid()
	simulated := Lease + PollInterval*6
	for elapsed := PollInterval; elapsed <= simulated; elapsed += PollInterval {
		now = now.Add(PollInterval)
		c.Reconcile(ctx)
		waitAndroid()
	}
	c.WaitDispatches()
	if androidRenewals.Load() < 3 {
		t.Fatalf("expected several android renewals during iOS hold, got %d", androidRenewals.Load())
	}
}
