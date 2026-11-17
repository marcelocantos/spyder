// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package memguard

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRun_TripsOncePastLimit(t *testing.T) {
	var heap atomic.Uint64
	heap.Store(100 << 20)
	tripped := make(chan uint64, 2)
	done := make(chan struct{})
	go func() {
		Run(t.Context(), Args{
			Limit: 1 << 30, Interval: time.Millisecond,
			Heap:     heap.Load,
			OnExceed: func(h, _ uint64) { tripped <- h },
		})
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case h := <-tripped:
		t.Fatalf("tripped under the limit at %d", h)
	default:
	}
	heap.Store(5 << 30)
	select {
	case h := <-tripped:
		if h != 5<<30 {
			t.Fatalf("heap %d", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not trip")
	}
	<-done // Run returns after tripping
}

func TestRun_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		Run(ctx, Args{Limit: 1 << 30, Interval: time.Millisecond, Heap: func() uint64 { return 0 }})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop")
	}
}

func TestLimitFromEnv(t *testing.T) {
	t.Setenv(LimitEnv, "512")
	if got := LimitFromEnv(); got != 512<<20 {
		t.Fatalf("%d", got)
	}
	t.Setenv(LimitEnv, "junk")
	if got := LimitFromEnv(); got != DefaultLimit {
		t.Fatalf("%d", got)
	}
}

// The real reader reports a heap that grows with live allocations.
func TestLiveHeap_Grows(t *testing.T) {
	before := liveHeap()
	keep := make([][]byte, 0, 64)
	for range 64 {
		keep = append(keep, make([]byte, 1<<20))
	}
	if after := liveHeap(); after < before+32<<20 {
		t.Fatalf("before %d after %d", before, after)
	}
	_ = keep[len(keep)-1]
}
