package mcp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// 🎯: multi-step app_exec must not be killed at 90s by the slow-dispatch
// ticker. Cancellation is owned by withToolDeadline only.
func TestWatchSlowDispatch_DoesNotCancelEarly(t *testing.T) {
	saveT, saveI := slowDispatchThreshold, slowDispatchInterval
	slowDispatchThreshold = 20 * time.Millisecond
	slowDispatchInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		slowDispatchThreshold = saveT
		slowDispatchInterval = saveI
	})

	var cancelled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	wrapped := func() {
		cancelled.Store(true)
		cancel()
	}

	done := make(chan struct{})
	go watchSlowDispatch("app_exec", "A23", time.Now(), done, wrapped)

	// Wait well past threshold+several intervals.
	time.Sleep(120 * time.Millisecond)
	if cancelled.Load() {
		t.Fatal("watchSlowDispatch cancelled the context early — multi-minute app_exec would die at ~90s")
	}
	if ctx.Err() != nil {
		t.Fatalf("context cancelled: %v", ctx.Err())
	}
	close(done)
}
