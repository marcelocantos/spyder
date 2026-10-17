// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devpolicy

import (
	"errors"
	"maps"
	"strings"
	"time"
)

const MaxSessionLease = 30 * time.Second
const MaxSessionOverrides = 512

// SessionOverride is deliberately never written to Store. Its identity survives
// a game cold boot, but never a host run's missed heartbeat or daemon restart.
type SessionOverride struct {
	Owner     string    `json:"owner"`
	RunToken  string    `json:"run_token"`
	DeviceID  string    `json:"device_id"`
	BundleID  string    `json:"bundle_id"`
	Flags     Flags     `json:"flags"`
	ExpiresAt time.Time `json:"expires_at"`
}

type sessionKey struct{ device, bundle string }

func (c *Controller) clock() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (s SessionOverride) validate() error {
	for _, v := range []string{s.Owner, s.RunToken, s.DeviceID, s.BundleID} {
		if strings.TrimSpace(v) != v || v == "" || len(v) > MaxOwnerBytes || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("session owner, run_token, device_id and bundle_id must be nonempty bounded identities")
		}
	}
	return nil
}

func (c *Controller) pruneSessions(now time.Time) {
	for k, s := range c.sessions {
		if !now.Before(s.ExpiresAt) {
			delete(c.sessions, k)
		}
	}
}

func (c *Controller) SessionOverrides() []SessionOverride {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneSessions(c.clock())
	out := make([]SessionOverride, 0, len(c.sessions))
	for _, s := range c.sessions {
		s.Flags = maps.Clone(s.Flags)
		out = append(out, s)
	}
	return out
}

// SetSession and ClearSession serialize with dispatch: after clear returns, no
// worker can send an older snapshot and revive the removed override.
func (c *Controller) SetSession(s SessionOverride, lease time.Duration) error {
	if err := s.validate(); err != nil {
		return err
	}
	if lease <= 0 || lease > MaxSessionLease {
		return errors.New("lease_ms must be between 1 and 30000")
	}
	if len(s.Flags) != 1 || (s.Flags["ads"] != "suppressed" && s.Flags["ads"] != "normal") {
		return errors.New("session flags must contain exactly ads: suppressed or normal")
	}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	if c.AuthorizeSession == nil {
		return errors.New("session overrides require reservation authorization")
	}
	if err := c.AuthorizeSession(s.DeviceID, s.Owner); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	c.pruneSessions(now)
	k := sessionKey{s.DeviceID, s.BundleID}
	if previous, ok := c.sessions[k]; ok && (previous.Owner != s.Owner || previous.RunToken != s.RunToken) {
		return errors.New("another run owns the active session override")
	}
	if c.sessions == nil {
		c.sessions = map[sessionKey]SessionOverride{}
	}
	if _, ok := c.sessions[k]; !ok && len(c.sessions) >= MaxSessionOverrides {
		return errors.New("too many session overrides")
	}
	s.Flags = maps.Clone(s.Flags)
	s.ExpiresAt = now.Add(lease)
	c.sessions[k] = s
	return nil
}

func (c *Controller) ClearSession(s SessionOverride) error {
	if err := s.validate(); err != nil {
		return err
	}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	if c.AuthorizeSession == nil {
		return errors.New("session overrides require reservation authorization")
	}
	if err := c.AuthorizeSession(s.DeviceID, s.Owner); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneSessions(c.clock())
	k := sessionKey{s.DeviceID, s.BundleID}
	if previous, ok := c.sessions[k]; ok && (previous.Owner != s.Owner || previous.RunToken != s.RunToken) {
		return errors.New("another run owns the active session override")
	}
	delete(c.sessions, k)
	return nil
}
