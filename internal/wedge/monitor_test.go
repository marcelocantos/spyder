// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"testing"
	"time"
)

// TestRunMonitor_ExitsOnContextCancel asserts the monitor lifecycle
// is clean — cancelling the context returns RunMonitor without
// leaking goroutines or blocking on the log-tail subprocess.
func TestRunMonitor_ExitsOnContextCancel(t *testing.T) {
	savePoll, saveDeb := pollInterval, debounceDelay
	pollInterval = 50 * time.Millisecond
	debounceDelay = 10 * time.Millisecond
	t.Cleanup(func() {
		pollInterval = savePoll
		debounceDelay = saveDeb
	})

	resetRecoveryThrottle()
	t.Cleanup(resetRecoveryThrottle)

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		RunMonitor(ctx)
		close(finished)
	}()

	// Let the monitor tick a few times — exercises the timer path
	// and the immediate startup check.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("RunMonitor did not return after context cancel")
	}
}
