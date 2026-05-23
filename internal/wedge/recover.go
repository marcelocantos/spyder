// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// recoveryHelperPath is the brew flat-link path of spyder-killusbmuxd.
// The path matches the sudoers entry installed by `spyder doctor
// --install-sudoers`; invoking via this exact path (rather than the
// Cellar-symlinked target) is what makes sudoers's NOPASSWD line
// match.
const recoveryHelperPath = "/opt/homebrew/bin/spyder-killusbmuxd"

// recoveryThrottle bounds how often the daemon re-attempts recovery.
// If a recovery succeeds but the wedge recurs immediately, something
// other than the documented phantom-disconnect bug is at play and
// looping kills of usbmuxd won't help — they'll just amplify the
// outage. Two minutes is generous enough to let launchd respawn,
// devices re-enumerate, and a few probe cycles confirm health.
const recoveryThrottle = 2 * time.Minute

var (
	recoveryMu   sync.Mutex
	lastRecovery time.Time
)

// AttemptRecovery invokes `sudo -n spyder-killusbmuxd` to restart
// usbmuxd. Returns fired=false (no-op) when the most recent attempt
// was within recoveryThrottle. The `-n` flag makes sudo fail fast
// if no sudoers entry is in place, instead of hanging on a password
// prompt the daemon can't satisfy.
//
// Outcomes are reported via slog:
//   - Info on success (usbmuxd was killed; launchd will respawn it).
//   - Error on sudo-not-configured or helper-execution failure.
//
// The sudoers entry installed via `spyder doctor --install-sudoers`
// is the de-facto opt-in for auto-recovery — when it isn't present,
// every attempt fails cleanly with sudo's non-interactive error and
// the wedge persists until a manual intervention.
func AttemptRecovery(ctx context.Context) (fired bool, err error) {
	recoveryMu.Lock()
	if !lastRecovery.IsZero() && time.Since(lastRecovery) < recoveryThrottle {
		remaining := recoveryThrottle - time.Since(lastRecovery)
		recoveryMu.Unlock()
		slog.Info("wedge: auto-recovery skipped (throttled)",
			"throttle_remaining_s", int(remaining.Seconds()))
		return false, nil
	}
	lastRecovery = time.Now()
	recoveryMu.Unlock()

	cmd := exec.CommandContext(ctx, "sudo", "-n", recoveryHelperPath)
	out, runErr := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if runErr != nil {
		slog.Error("wedge: auto-recovery failed",
			"helper", recoveryHelperPath,
			"error", runErr.Error(),
			"output", trimmed)
		return true, fmt.Errorf("sudo %s: %w", recoveryHelperPath, runErr)
	}
	slog.Info("wedge: auto-recovery invoked",
		"helper", recoveryHelperPath, "output", trimmed)
	return true, nil
}

// resetRecoveryThrottle is a test hook. Production code never calls
// this; tests use it to bypass the 2-minute gate between attempts.
func resetRecoveryThrottle() {
	recoveryMu.Lock()
	lastRecovery = time.Time{}
	recoveryMu.Unlock()
}
