// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// A registry that answers is left alone; once it stops answering, the
// daemon starts its own tunnel exactly once.
func TestTakeOverTunnel_StartsOnceRegistryGoes(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	var starts atomic.Int32
	done := make(chan struct{})
	go func() {
		takeOverTunnel(context.Background(), up.Load, func() error {
			starts.Add(1)
			return nil
		}, time.Millisecond)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	if n := starts.Load(); n != 0 {
		t.Fatalf("started %d tunnels while the registry still answered", n)
	}
	up.Store(false)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no takeover after the registry went away")
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
}

// A failed start is retried; cancellation ends the watch.
func TestTakeOverTunnel_RetriesAndStopsOnCancel(t *testing.T) {
	var starts atomic.Int32
	ok := takeOverTunnelUntil(t, func() error {
		if starts.Add(1) < 3 {
			return errors.New("bind: address in use")
		}
		return nil
	})
	if !ok || starts.Load() != 3 {
		t.Fatalf("ok=%v starts=%d, want 3 attempts", ok, starts.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		takeOverTunnel(ctx, func() bool { return true }, func() error { return nil }, time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("takeover watch ignored cancellation")
	}
}

func takeOverTunnelUntil(t *testing.T, start func() error) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		takeOverTunnel(context.Background(), func() bool { return false }, start, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}
