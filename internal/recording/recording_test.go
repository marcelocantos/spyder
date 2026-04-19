// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"errors"
	"testing"
	"time"
)

func makeSession(t *testing.T, reg *Registry, device, owner string) *Session {
	t.Helper()
	done := make(chan struct{})
	s, err := reg.Start(device, owner, "/tmp/test.mp4", func() error {
		close(done)
		return nil
	}, done)
	if err != nil {
		t.Fatalf("Start(%q, %q): %v", device, owner, err)
	}
	return s
}

func TestStart_HappyPath(t *testing.T) {
	reg := NewRegistry()
	s := makeSession(t, reg, "Pippa", "tiltbuggy")
	if s.Device != "Pippa" {
		t.Errorf("Device = %q; want Pippa", s.Device)
	}
	if s.Owner != "tiltbuggy" {
		t.Errorf("Owner = %q; want tiltbuggy", s.Owner)
	}
}

func TestStart_Conflict(t *testing.T) {
	reg := NewRegistry()
	makeSession(t, reg, "Pippa", "alpha")

	done := make(chan struct{})
	_, err := reg.Start("Pippa", "beta", "/tmp/other.mp4", func() error { return nil }, done)
	if err == nil {
		t.Fatal("expected ErrConflict; got nil")
	}
	var c *ErrConflict
	if !errors.As(err, &c) {
		t.Fatalf("expected *ErrConflict; got %T: %v", err, err)
	}
	if c.CurrentOwner != "alpha" {
		t.Errorf("CurrentOwner = %q; want alpha", c.CurrentOwner)
	}
}

func TestStop_RemovesFromRegistry(t *testing.T) {
	reg := NewRegistry()
	makeSession(t, reg, "Pippa", "tiltbuggy")

	s, err := reg.Stop("Pippa")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if s == nil {
		t.Fatal("Stop returned nil session")
	}

	// Registry should be clear now.
	if reg.ForDevice("Pippa") != nil {
		t.Error("ForDevice(Pippa) should return nil after Stop")
	}
}

func TestStop_NotRecording(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Stop("Pippa")
	if err == nil {
		t.Fatal("expected error stopping non-existent session")
	}
}

func TestStop_CallsStopFn(t *testing.T) {
	reg := NewRegistry()
	called := false
	done := make(chan struct{})
	_, err := reg.Start("Pippa", "alpha", "/tmp/x.mp4", func() error {
		called = true
		close(done)
		return nil
	}, done)
	if err != nil {
		t.Fatal(err)
	}

	_, stopErr := reg.Stop("Pippa")
	if stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stopFn was not called within 1s")
	}
	if !called {
		t.Error("stopFn was not called")
	}
}

func TestGet_ReturnsSession(t *testing.T) {
	reg := NewRegistry()
	makeSession(t, reg, "Pippa", "alpha")
	s, err := reg.Get("Pippa")
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("Get returned nil for active session")
	}
	if s.Owner != "alpha" {
		t.Errorf("Owner = %q; want alpha", s.Owner)
	}
}

func TestGet_NotFound(t *testing.T) {
	reg := NewRegistry()
	s, err := reg.Get("Pippa")
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Errorf("Get should return nil for no session; got %+v", s)
	}
}

func TestIsConflict(t *testing.T) {
	err := &ErrConflict{Device: "Pippa", CurrentOwner: "alpha"}
	if !IsConflict(err) {
		t.Error("IsConflict should return true for *ErrConflict")
	}
	if IsConflict(nil) {
		t.Error("IsConflict should return false for nil")
	}
}

func TestForOwner(t *testing.T) {
	reg := NewRegistry()
	makeSession(t, reg, "Pippa", "alpha")
	makeSession(t, reg, "Raspberry", "beta")

	sessions := reg.ForOwner("alpha")
	if len(sessions) != 1 || sessions[0].Device != "Pippa" {
		t.Errorf("ForOwner(alpha) = %v; want [Pippa]", sessions)
	}

	sessions = reg.ForOwner("beta")
	if len(sessions) != 1 || sessions[0].Device != "Raspberry" {
		t.Errorf("ForOwner(beta) = %v; want [Raspberry]", sessions)
	}

	sessions = reg.ForOwner("nobody")
	if len(sessions) != 0 {
		t.Errorf("ForOwner(nobody) = %v; want []", sessions)
	}
}

func TestStartAfterStop(t *testing.T) {
	reg := NewRegistry()
	makeSession(t, reg, "Pippa", "alpha")
	reg.Stop("Pippa") //nolint:errcheck

	// Should be able to start again after stop.
	s := makeSession(t, reg, "Pippa", "beta")
	if s.Owner != "beta" {
		t.Errorf("Owner = %q; want beta", s.Owner)
	}
}
