// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build device

package pmd3bridge

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestDevice_StaysAwake_Mechanical is the 🎯T29 acceptance test.
//
// Contract:
//  1. Acquire a power assertion (PreventUserIdleSystemSleep) on the device.
//  2. Wait 60 s — long enough to exceed the device's auto-lock timeout if
//     no assertion were held.
//  3. Query /v1/device_power_state → expect "awake".
//  4. Release the assertion; wait another 60 s (no guard).
//  5. Query again → expect "display_off" or "asleep".
//
// Step 5 requires the device's Settings → Display & Brightness → Auto-Lock
// to be ≤ 30 s. The test skips if the precondition cannot be verified.
//
// HIL verification status (2026-04-26): PENDING. The assertion/query
// infrastructure is implemented but the test has not yet been run against
// a real device to confirm that:
//   - the screenshot call does NOT reset the idle timer, and
//   - the bridge correctly classifies the display-off exception shape from pmd3.
//
// HIL run protocol:
//  1. On the device (Pippa preferred): set Settings → Display & Brightness
//     → Auto-Lock to 30 seconds. Confirm Developer Mode is enabled and the
//     device is paired and tunneled.
//  2. Export SPYDER_T29_HIL=1 (gates the test) and optionally
//     SPYDER_TEST_UDID=<pippa-udid> to pin a specific device.
//  3. Lay the device flat, screen visible, and don't touch it during the
//     2-minute run. Phase 2 needs the auto-lock idle timer to expire.
//
// Once HIL-verified across a few runs, the env-var gate can be lifted
// (or kept as a safety so CI without a device doesn't accidentally try).
func TestDevice_StaysAwake_Mechanical(t *testing.T) {
	if os.Getenv("SPYDER_T29_HIL") != "1" {
		t.Skip("🎯T29: HIL run gated — set SPYDER_T29_HIL=1 (and ideally SPYDER_TEST_UDID=<pippa>) to run; needs auto-lock ≤30s and ~2 min undisturbed")
	}

	_, c := startRealBridge(t)
	d := firstIOSDevice(t, c)
	ctx := context.Background()

	// ── Phase 1: assertion held — device should stay awake ──────────────────

	t.Logf("acquiring power assertion on %s (%s)", d.Name, d.UDID)
	handle, err := c.AcquirePowerAssertion(ctx, d.UDID,
		"PreventUserIdleSystemSleep", "spyder T29 staysawake test", 300, "")
	if err != nil {
		t.Fatalf("AcquirePowerAssertion: %v", err)
	}
	t.Logf("assertion acquired: handle=%s", handle)

	t.Logf("waiting 60 s with assertion held...")
	time.Sleep(60 * time.Second)

	state, err := c.DevicePowerState(ctx, d.UDID)
	if err != nil {
		t.Fatalf("DevicePowerState (with assertion): %v", err)
	}
	t.Logf("phase 1 state=%s detail=%v", state.State, state.Detail)
	if state.State != "awake" {
		t.Errorf("phase 1: expected awake with assertion held; got %q (detail: %v)",
			state.State, state.Detail)
	}

	// ── Phase 2: assertion released — device should auto-lock ───────────────

	t.Logf("releasing assertion and waiting 60 s without guard...")
	if err := c.ReleasePowerAssertion(ctx, handle); err != nil {
		t.Errorf("ReleasePowerAssertion: %v", err)
	}

	time.Sleep(60 * time.Second)

	state2, err := c.DevicePowerState(ctx, d.UDID)
	if err != nil {
		t.Fatalf("DevicePowerState (without assertion): %v", err)
	}
	t.Logf("phase 2 state=%s detail=%v", state2.State, state2.Detail)
	if state2.State != "display_off" && state2.State != "asleep" {
		t.Errorf("phase 2: expected display_off or asleep without assertion; got %q (detail: %v)",
			state2.State, state2.Detail)
	}
}
