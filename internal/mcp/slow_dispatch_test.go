// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"
	"time"
)

// TestWatchSlowDispatch_ReturnsWhenDoneCloses verifies the watchdog
// goroutine exits cleanly once done is closed, even after it has
// already fired its first slow-call warning. Tight thresholds let
// the test exercise both the pre-threshold and post-threshold
// branches of the select.
func TestWatchSlowDispatch_ReturnsWhenDoneCloses(t *testing.T) {
	saveT, saveI := slowDispatchThreshold, slowDispatchInterval
	slowDispatchThreshold = 10 * time.Millisecond
	slowDispatchInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		slowDispatchThreshold = saveT
		slowDispatchInterval = saveI
	})

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		watchSlowDispatch("test", "", time.Now(), done, nil)
		close(finished)
	}()

	// Let it fire at least once past the threshold.
	time.Sleep(30 * time.Millisecond)
	close(done)

	select {
	case <-finished:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("watchSlowDispatch did not return after done closed")
	}
}

// TestWatchSlowDispatch_ExitsBeforeThresholdWhenAlreadyDone verifies
// the common fast-call path: done fires before the slow threshold,
// the watchdog returns immediately, no log noise.
func TestWatchSlowDispatch_ExitsBeforeThresholdWhenAlreadyDone(t *testing.T) {
	saveT := slowDispatchThreshold
	slowDispatchThreshold = time.Hour
	t.Cleanup(func() { slowDispatchThreshold = saveT })

	done := make(chan struct{})
	close(done)

	finished := make(chan struct{})
	go func() {
		watchSlowDispatch("test", "", time.Now(), done, nil)
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("watchSlowDispatch did not return immediately when done was already closed")
	}
}
