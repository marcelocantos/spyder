// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"errors"
	"github.com/danielpaulus/go-ios/ios"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolverContext_CancelQueuedWaitDoesNotCancelOwnerOrOtherDevice(t *testing.T) {
	output := captureTrace(t)
	r := New(DefaultTunnelHost, DefaultTunnelPort)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r.resolveFn = func(id string) (ios.DeviceEntry, int, error) {
		calls.Add(1)
		if id == "one" {
			close(entered)
			<-release
		}
		return ios.DeviceEntry{}, 27, nil
	}
	owner := make(chan error, 1)
	go func() { _, _, err := r.SessionWithVersion("one"); owner <- err }()
	<-entered
	ctx, cancel := context.WithCancel(LifecycleOperation(context.Background(), 21, "terminate_app", "one"))
	waiter := make(chan error, 1)
	var factories atomic.Int32
	pool := NewServicePool(r, func(ios.DeviceEntry) (int, error) { factories.Add(1); return 1, nil }, func(int) error { return nil }, time.Minute)
	defer pool.Close()
	go func() { _, err := pool.AcquireLease(ctx, "one"); waiter <- err }()
	waitTraceEvent(t, output, "resolver.wait")
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter remained blocked")
	}
	if _, _, err := r.SessionWithVersionContext(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-owner:
		t.Fatalf("waiter disturbed owner: %v", err)
	default:
	}
	close(release)
	if err := <-owner; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || factories.Load() != 0 {
		t.Fatalf("resolver work=%d unexpected waiter factories=%d", calls.Load(), factories.Load())
	}
}
func TestResolverContext_CancelledEntryDoesNotStartSetup(t *testing.T) {
	r := New(DefaultTunnelHost, DefaultTunnelPort)
	r.resolveFn = func(string) (ios.DeviceEntry, int, error) {
		t.Fatal("cancelled call started setup")
		return ios.DeviceEntry{}, 0, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.SessionWithVersionContext(ctx, "one"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// This is an explicit residual counterexample, not a setup-cancellation pass.
// The owning opaque provider does not expose its socket before return; no
// caller-visible timeout wrapper is allowed to pretend that work was reaped.
func TestResolverContext_OpaqueOwnedSetupRemainsPendingUntilProviderDrains(t *testing.T) {
	r := New(DefaultTunnelHost, DefaultTunnelPort)
	entered, release := make(chan struct{}), make(chan struct{})
	r.resolveFn = func(string) (ios.DeviceEntry, int, error) {
		close(entered)
		<-release
		return ios.DeviceEntry{}, 27, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := r.SessionWithVersionContext(ctx, "one"); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("opaque setup claimed cancellation before drain: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("drained owner did not finish")
	}
	if _, _, err := r.SessionWithVersion("one"); err != nil {
		t.Fatal(err)
	}
}
