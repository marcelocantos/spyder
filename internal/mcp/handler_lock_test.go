// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// DH-06: device I/O must not run while h.mu is held. A slow crash pull
// used to stall an inventory resolve (and every other verb) behind it.
func TestSlowCrashesDoesNotBlockResolve(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	ios := &stubAdapter{crashes: func(string, time.Time, string) ([]device.CrashReport, error) {
		close(started)
		<-release
		return nil, nil
	}}
	h := newHandlerWithStubs(t, ios, nil)
	t.Cleanup(func() { close(release) })

	go func() {
		_, _ = h.handleCrashes(map[string]any{"device": "iPad"})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("crashes did not start")
	}

	done := make(chan time.Duration, 1)
	go func() {
		t0 := time.Now()
		res, err := h.handleResolve(map[string]any{"name": "iPad"})
		if err != nil || res == nil || res.IsError {
			t.Errorf("resolve failed: err=%v res=%v", err, res)
		}
		done <- time.Since(t0)
	}()
	select {
	case d := <-done:
		if d > 200*time.Millisecond {
			t.Fatalf("resolve waited %v behind a crash pull", d)
		}
	case <-time.After(time.Second):
		t.Fatal("resolve blocked behind a crash pull")
	}
}

func TestSlowListDoesNotBlockResolve(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	ios := &stubAdapter{list: func() ([]device.Info, error) {
		close(started)
		<-release
		return []device.Info{{UUID: "dev-1", Name: "Phone", Platform: "ios"}}, nil
	}}
	h := newHandlerWithStubs(t, ios, nil)
	t.Cleanup(func() { close(release) })

	go func() {
		_, _ = h.handleResolve(map[string]any{"selector": `{"platform":"ios"}`})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("selector list did not start")
	}

	done := make(chan struct{})
	go func() {
		_, _ = h.handleResolve(map[string]any{"name": "iPad"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("name resolve blocked behind a selector's device list")
	}
}
