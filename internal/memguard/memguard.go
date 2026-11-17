// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package memguard stops the daemon before a memory runaway takes the
// host down (🎯T195).
//
// On 15 Nov 2026 a device connection that died mid app listing left
// go-ios's installation_proxy browse loop appending empty responses
// forever: the daemon went from 35 MB to 81 GB in five minutes. The
// go-ios fork now returns the read error, but any decoder that sizes an
// allocation from the wire, or any future loop of the same shape, can do
// it again. The guard samples the live heap and, past the limit, writes
// evidence and exits so launchd restarts a clean daemon. Unlike the
// stuck-dispatch self-restart it is not rate-limited: a process that
// keeps allocating has to go.
package memguard

import (
	"context"
	"log/slog"
	"os"
	"runtime/metrics"
	"strconv"
	"time"
)

const (
	// DefaultLimit is the heap at which the daemon gives up. It normally
	// runs at well under 200 MB.
	DefaultLimit = 4 << 30
	// DefaultInterval is how often the heap is sampled. At the 15 Nov
	// growth rate (~15 GB/min) the guard trips within a couple of seconds
	// of crossing the limit.
	DefaultInterval = 2 * time.Second
	// LimitEnv overrides the limit, in MiB.
	LimitEnv = "SPYDER_HEAP_LIMIT_MB"
)

// Args configures Run.
type Args struct {
	Limit    uint64        // bytes; DefaultLimit (or LimitEnv) when zero
	Interval time.Duration // DefaultInterval when zero
	// Heap reads the live heap in bytes; defaults to runtime/metrics.
	Heap func() uint64
	// OnExceed runs once when the heap passes the limit. Production writes
	// evidence and exits; Run returns after it.
	OnExceed func(heap, limit uint64)
}

// LimitFromEnv returns the limit from LimitEnv, or DefaultLimit.
func LimitFromEnv() uint64 {
	if v := os.Getenv(LimitEnv); v != "" {
		if mb, err := strconv.ParseUint(v, 10, 64); err == nil && mb > 0 {
			return mb << 20
		}
		slog.Warn("memguard: ignoring bad "+LimitEnv, "value", v)
	}
	return DefaultLimit
}

// Run samples the heap until ctx ends or the limit is passed.
func Run(ctx context.Context, args Args) {
	limit := args.Limit
	if limit == 0 {
		limit = LimitFromEnv()
	}
	interval := args.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	heap := args.Heap
	if heap == nil {
		heap = liveHeap
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if h := heap(); h > limit {
			slog.Error("memguard: heap over limit; exiting for a clean restart", "heap_mb", h>>20, "limit_mb", limit>>20)
			if args.OnExceed != nil {
				args.OnExceed(h, limit)
			}
			return
		}
	}
}

// liveHeap is the bytes in live and not-yet-swept heap objects. Reading
// runtime/metrics does not stop the world, unlike ReadMemStats.
func liveHeap() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}
