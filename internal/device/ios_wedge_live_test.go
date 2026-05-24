// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// killUSBMuxdHelper is the bundled helper `spyder doctor --install-sudoers`
// authorises for passwordless sudo. The wedge test invokes it to take
// usbmuxd down at setup.
const killUSBMuxdHelper = "/opt/homebrew/bin/spyder-killusbmuxd"

// TestIOSWedgedMode_Live is the headline 🎯T72 attestation: with usbmuxd
// intentionally killed, the CoreDevice/devicectl-backed surface (list_apps,
// resolve, launch, pid, terminate) must keep working, while the DTX-only
// surface (screenshot, oslog stream) degrades with a structured
// usbmuxd-unavailable error.
//
// Doubly gated: SPYDER_LIVE_UDID selects the device, and SPYDER_LIVE_WEDGE=1
// is an explicit opt-in because killing usbmuxd disrupts every other tool
// that talks to iOS devices on the host. Requires the passwordless sudoers
// entry from `spyder doctor --install-sudoers`.
//
// Nuance on the DTX assertions: launchd respawns usbmuxd within ~1s, and an
// iOS-17+ RSD tunnel that's already established can outlive a usbmuxd kill
// (it rides a utun interface). So Screenshot/LogRange are checked
// best-effort — if they error, the error MUST be the structured degradation
// (not something else); if they unexpectedly succeed, the tunnel simply
// survived the kill and the test logs that rather than flaking. The
// deterministic degraded-error contract is pinned by the hermetic unit
// tests (TestScreenshotDegradesWhenTunnelDown et al.).
func TestIOSWedgedMode_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live iOS test")
	}
	if os.Getenv("SPYDER_LIVE_WEDGE") != "1" {
		t.Skip("SPYDER_LIVE_WEDGE != 1; skipping (this test kills usbmuxd host-wide)")
	}

	a := NewIOSAdapter()
	bundleID := liveIOSLaunchBundle(t, a, udid) // skips if fixture absent

	// Take usbmuxd down.
	if out, err := exec.Command("sudo", "-n", killUSBMuxdHelper).CombinedOutput(); err != nil {
		t.Skipf("could not kill usbmuxd via %s (need `spyder doctor --install-sudoers`): %v\n%s",
			killUSBMuxdHelper, err, out)
	}
	t.Logf("usbmuxd killed via %s; exercising devicectl surface immediately", killUSBMuxdHelper)

	// --- Firm assertions: devicectl/CoreDevice ops survive the wedge. ---

	if _, err := a.ListApps(udid); err != nil {
		t.Errorf("ListApps must work with usbmuxd down (devicectl path): %v", err)
	}
	if _, installed, err := a.ResolveExecutable(udid, bundleID); err != nil || !installed {
		t.Errorf("ResolveExecutable must work with usbmuxd down: installed=%v err=%v", installed, err)
	}
	if err := a.LaunchApp(udid, bundleID); err != nil {
		t.Errorf("LaunchApp must work with usbmuxd down (devicectl path): %v", err)
	}
	var pid int
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if p, err := a.AppPID(udid, bundleID); err == nil && p > 0 {
			pid = p
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if pid <= 0 {
		t.Errorf("AppPID didn't resolve with usbmuxd down (devicectl path)")
	}
	if err := a.TerminateApp(udid, bundleID); err != nil {
		t.Errorf("TerminateApp must work with usbmuxd down (devicectl path): %v", err)
	}

	// --- Best-effort: DTX-only surface degrades cleanly when it can't reach
	// the tunnel/usbmuxd. ---

	checkDegraded := func(tool string, err error) {
		switch {
		case err == nil:
			t.Logf("%s succeeded despite the usbmuxd kill — the RSD tunnel outlived it; "+
				"degraded-error contract is pinned by the hermetic unit tests", tool)
		case errors.Is(err, ErrUSBMuxdUnavailable):
			msg := err.Error()
			if !strings.Contains(msg, "usbmuxd") || !strings.Contains(msg, "unavailable") {
				t.Errorf("%s degraded error missing matchable substrings: %q", tool, msg)
			} else {
				t.Logf("%s degraded as expected: %v", tool, err)
			}
		default:
			t.Errorf("%s failed with a non-degraded error (want ErrUSBMuxdUnavailable): %v", tool, err)
		}
	}

	_, ssErr := a.Screenshot(udid)
	checkDegraded("screenshot", ssErr)

	_, lrErr := a.LogRange(udid, LogFilter{}, time.Time{}, time.Now().Add(1500*time.Millisecond))
	checkDegraded("logs", lrErr)

	// Give launchd a beat to respawn usbmuxd so we don't leave the host
	// worse off than we found it for the next test.
	time.Sleep(2 * time.Second)
}
