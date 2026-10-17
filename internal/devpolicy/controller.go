// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devpolicy

import (
	"context"
	"maps"
	"strings"
	"sync"
	"time"
)

const PollInterval = 2 * time.Second
const Lease = 10 * time.Second
const CallTimeout = time.Second
const MaxProofAge = PollInterval

type Peer struct {
	ID, DeviceID, BundleID string
	Capable                bool
	AdsCapable             bool
}
type Status struct {
	SessionID        string     `json:"session_id"`
	DeviceID         string     `json:"device_id"`
	BundleID         string     `json:"bundle_id"`
	Capable          bool       `json:"capable"`
	AdsCapable       bool       `json:"ads_capable"`
	UnsupportedFlags Flags      `json:"unsupported_flags,omitempty"`
	RunToken         string     `json:"run_token,omitempty"`
	USB              bool       `json:"physical_usb"`
	Configured       Resolution `json:"configured"`
	Applied          Flags      `json:"applied"`
	Ack              any        `json:"ack,omitempty"`
	Error            string     `json:"error,omitempty"`
	LeaseExpiresAt   time.Time  `json:"lease_expires_at,omitempty"`
	CheckedAt        time.Time  `json:"checked_at"`
}

type Controller struct {
	Store            *Store
	Peers            func() []Peer
	USB              func(context.Context) (map[string]bool, error)
	Authorize        func(device, owner string) error
	AuthorizeSession func(device, owner string) error
	Call             func(context.Context, Peer, Flags, time.Duration) (any, error)
	Now              func() time.Time
	reconcileMu      sync.Mutex
	mu               sync.Mutex
	sessions         map[sessionKey]SessionOverride
	statuses         map[string]Status
}

func (c *Controller) Statuses() []Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Status, 0, len(c.statuses))
	for _, s := range c.statuses {
		s.Applied = maps.Clone(s.Applied)
		s.UnsupportedFlags = maps.Clone(s.UnsupportedFlags)
		s.Configured = Resolution{Flags: maps.Clone(s.Configured.Flags), Provenance: maps.Clone(s.Configured.Provenance)}
		out = append(out, s)
	}
	return out
}

func (c *Controller) Run(ctx context.Context) {
	c.Reconcile(ctx)
	tick := time.NewTicker(PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.Reconcile(ctx)
		}
	}
}

// Reconcile never trusts a cached USB observation. Failure prevents renewal;
// a reachable peer is explicitly revoked, and an unreachable lease expires.
func (c *Controller) Reconcile(ctx context.Context) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	cfg, configErr := c.Store.Load()
	peers := c.Peers()
	if len(peers) == 0 {
		c.mu.Lock()
		c.statuses = map[string]Status{}
		c.mu.Unlock()
		return
	}
	proofAt := time.Now() // monotonic; oldest possible observation in this census
	proofCtx, cancel := context.WithTimeout(ctx, CallTimeout)
	usb, proofErr := c.USB(proofCtx)
	cancel()
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	next := map[string]Status{}
	var pending sync.WaitGroup
	var nextMu sync.Mutex
	for _, p := range peers {
		pending.Add(1)
		go func(p Peer) {
			defer pending.Done()
			c.mu.Lock()
			prev, seen := c.statuses[p.ID]
			c.mu.Unlock()
			s := Status{SessionID: p.ID, DeviceID: p.DeviceID, BundleID: p.BundleID, Capable: p.Capable, AdsCapable: p.AdsCapable, USB: (usb[p.DeviceID] || usb[strings.ToUpper(strings.ReplaceAll(p.DeviceID, "-", ""))]) && proofErr == nil, Configured: cfg.Resolve(p.BundleID, nil), Applied: maps.Clone(prev.Applied), Ack: prev.Ack, LeaseExpiresAt: prev.LeaseExpiresAt, CheckedAt: now}
			if !now.Before(s.LeaseExpiresAt) {
				s.Applied = Flags{}
				s.LeaseExpiresAt = time.Time{}
			}
			owner := cfg.Owner
			c.mu.Lock()
			c.pruneSessions(c.clock())
			override, overridden := c.sessions[sessionKey{p.DeviceID, p.BundleID}]
			c.mu.Unlock()
			if overridden {
				owner = override.Owner
				s.RunToken = override.RunToken
				s.Configured = cfg.Resolve(p.BundleID, override.Flags)
			}
			desired := maps.Clone(s.Configured.Flags)
			if !p.AdsCapable {
				if ads, ok := desired["ads"]; ok {
					s.UnsupportedFlags = Flags{"ads": ads}
					delete(desired, "ads") // Preserve older SDK orientation renewals.
				}
			}
			if configErr != nil {
				s.Error = "policy: " + configErr.Error()
				desired = Flags{}
			}
			if proofErr != nil {
				s.Error = "USB proof: " + proofErr.Error()
				desired = Flags{}
			}
			if !s.USB {
				desired = Flags{}
			}
			authorize := c.Authorize
			if overridden {
				authorize = c.AuthorizeSession
			}
			if overridden && authorize == nil {
				s.Error = "session reservation authorization unavailable"
				desired = Flags{}
			}
			if authorize != nil {
				if err := authorize(p.DeviceID, owner); err != nil {
					s.Error = err.Error()
					desired = Flags{}
				}
			}
			defer func() { nextMu.Lock(); next[p.ID] = s; nextMu.Unlock() }()
			if !p.Capable {
				return
			}
			// Check immediately before dispatch: a delayed worker must not renew
			// from a stale census. USB loss/error may still send an empty revoke.
			if time.Since(proofAt) > MaxProofAge {
				s.USB = false
				s.Error = "USB proof is stale"
				desired = Flags{}
			}
			if err := ctx.Err(); err != nil {
				s.Error = err.Error()
				desired = Flags{}
			}
			// Send a first revoke even after daemon restart. Do not repeatedly call
			// an unchanged empty policy, but renew every active lease.
			if len(desired) > 0 || !seen || len(prev.Applied) > 0 || prev.Error != "" {
				deadline := time.Now().Add(CallTimeout)
				if proofDeadline := proofAt.Add(MaxProofAge); len(desired) > 0 && proofDeadline.Before(deadline) {
					deadline = proofDeadline
				}
				callCtx, stop := context.WithDeadline(ctx, deadline)
				lease := Lease
				if overridden && len(desired) > 0 {
					remaining := override.ExpiresAt.Sub(c.clock()).Truncate(time.Millisecond)
					if remaining <= 0 {
						desired = Flags{}
					} else if remaining < lease {
						lease = remaining
					}
				}
				ack, err := c.Call(callCtx, p, desired, lease)
				stop()
				if err != nil {
					s.Error = "configure: " + err.Error()
				} else {
					s.Ack = ack
					s.Applied = maps.Clone(desired)
					if len(desired) > 0 {
						s.LeaseExpiresAt = now.Add(lease)
					} else {
						s.LeaseExpiresAt = time.Time{}
					}
				}
			}
		}(p)
	}
	pending.Wait()
	c.mu.Lock()
	c.statuses = next
	c.mu.Unlock()
}
