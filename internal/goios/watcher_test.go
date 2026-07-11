// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	events []UsbmuxEvent
	i      int
	closed bool
}

func (f *fakeSource) Next(ctx context.Context) (UsbmuxEvent, error) {
	if f.i >= len(f.events) {
		<-ctx.Done()
		return UsbmuxEvent{}, ctx.Err()
	}
	ev := f.events[f.i]
	f.i++
	return ev, nil
}

func (f *fakeSource) Close() error {
	f.closed = true
	return nil
}

type recRecovery struct {
	mu          sync.Mutex
	drops       []string
	reestablishes []string
	invalidates []string
	reErr       error
}

func (r *recRecovery) DropTunnel(udid string) {
	r.mu.Lock()
	r.drops = append(r.drops, udid)
	r.mu.Unlock()
}
func (r *recRecovery) ReestablishTunnel(udid string) error {
	r.mu.Lock()
	r.reestablishes = append(r.reestablishes, udid)
	err := r.reErr
	r.mu.Unlock()
	return err
}
func (r *recRecovery) Invalidate(udid string) {
	r.mu.Lock()
	r.invalidates = append(r.invalidates, udid)
	r.mu.Unlock()
}

type recPools struct {
	mu   sync.Mutex
	ids  []string
}

func (p *recPools) InvalidateDevice(udid string) {
	p.mu.Lock()
	p.ids = append(p.ids, udid)
	p.mu.Unlock()
}

func TestWatchUsbmux_DetachThenAttach(t *testing.T) {
	src := &fakeSource{events: []UsbmuxEvent{
		{Kind: UsbmuxDetach, UDID: "D1"},
		{Kind: UsbmuxAttach, UDID: "D1"},
	}}
	rec := &recRecovery{}
	pools := &recPools{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		WatchUsbmux(ctx, src, rec, pools)
		close(done)
	}()

	// Wait until both events processed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		nDrop, nRe := len(rec.drops), len(rec.reestablishes)
		rec.mu.Unlock()
		if nDrop >= 1 && nRe >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.drops) != 1 || rec.drops[0] != "D1" {
		t.Fatalf("drops=%v", rec.drops)
	}
	if len(rec.reestablishes) != 1 || rec.reestablishes[0] != "D1" {
		t.Fatalf("reestablishes=%v", rec.reestablishes)
	}
	if len(rec.invalidates) < 1 {
		t.Fatalf("expected invalidate on attach; got %v", rec.invalidates)
	}
	pools.mu.Lock()
	defer pools.mu.Unlock()
	if len(pools.ids) < 2 {
		t.Fatalf("expected pool invalidations on detach+attach; got %v", pools.ids)
	}
	if !src.closed {
		t.Error("source should be closed on watcher exit")
	}
}
