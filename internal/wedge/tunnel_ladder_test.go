// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestReconcile_TunnelStartLadder is the 🎯T99.4 production-path oracle:
// injected broken-pipe / tunnel-start log lines → detect → once recoverFn.
func TestReconcile_TunnelStartLadder(t *testing.T) {
	clearRecentLogs()
	t.Cleanup(clearRecentLogs)

	var recoverCalls atomic.Int32
	prevIs, prevCap, prevRec := isWedgedFn, captureFn, recoverFn
	t.Cleanup(func() {
		isWedgedFn, captureFn, recoverFn = prevIs, prevCap, prevRec
	})
	// Parity path healthy — only tunnel-start class fires.
	isWedgedFn = func() (bool, int, int, error) {
		return false, 0, 0, nil
	}
	captureFn = func(string, string) {}
	recoverFn = func(ctx context.Context) error {
		recoverCalls.Add(1)
		return nil
	}

	NoteLogLine(`write unix ->/var/run/usbmuxd: broken pipe`)
	NoteLogLine(`failed to start tunnel for device A`)
	NoteLogLine(`write unix ->/var/run/usbmuxd: broken pipe`)

	var st wedgeState
	reconcile(context.Background(), "test", &st)
	if recoverCalls.Load() != 1 {
		t.Fatalf("recover calls=%d want 1", recoverCalls.Load())
	}
	if !st.inEpisode || !st.attempted {
		t.Fatalf("episode state: in=%v attempted=%v", st.inEpisode, st.attempted)
	}
	// Second reconcile: no second recovery (needs_attention path).
	reconcile(context.Background(), "test", &st)
	if recoverCalls.Load() != 1 {
		t.Fatalf("second recover calls=%d want still 1", recoverCalls.Load())
	}
	f := LastDoctorFinding()
	if !f.Wedged {
		t.Fatal("shared doctor finding should be wedged")
	}
}
