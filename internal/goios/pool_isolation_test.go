// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios"
)

// TestServicePool_AcquireAllowsConcurrentHandshakes is the 🎯T99.2 structural
// oracle: newConn may run concurrently for different UDIDs. If p.mu were held
// across newConn, maxInFlight would stay 1.
//
// Skips when Session() cannot resolve the UDID (no tunnel / no device).
func TestServicePool_AcquireAllowsConcurrentHandshakes(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	release := make(chan struct{})

	p := NewServicePool(
		New(DefaultTunnelHost, DefaultTunnelPort),
		func(_ ios.DeviceEntry) (int, error) {
			n := inFlight.Add(1)
			for {
				old := maxInFlight.Load()
				if n <= old || maxInFlight.CompareAndSwap(old, n) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			return 1, nil
		},
		func(int) error { return nil },
		time.Minute,
	)
	t.Cleanup(func() { _ = p.Close() })

	// Probe whether Session works at all for a dummy id.
	if _, _, err := p.Acquire("probe-no-device"); err != nil {
		t.Skipf("Session resolve unavailable (expected without attached device/tunnel for fake UDID): %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for _, id := range []string{"udid-a", "udid-b"} {
		id := id
		go func() {
			defer wg.Done()
			_, rel, err := p.Acquire(id)
			if err != nil {
				return
			}
			rel()
		}()
	}
	// Give both a chance to enter newConn.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if maxInFlight.Load() < 2 {
		t.Fatalf("max concurrent newConn = %d want >= 2 (pool mutex held across handshake?)", maxInFlight.Load())
	}
}
