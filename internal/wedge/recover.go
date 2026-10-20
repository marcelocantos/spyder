// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// recoveryHelperPath is the brew flat-link path of spyder-killusbmuxd.
// The path matches the sudoers entry installed by `spyder doctor
// --install-sudoers`; invoking via this exact path (rather than the
// Cellar-symlinked target) is what makes sudoers's NOPASSWD line
// match.
const recoveryHelperPath = "/opt/homebrew/bin/spyder-killusbmuxd"

// AttemptRecovery invokes `sudo -n spyder-killusbmuxd` to restart usbmuxd.
// The `-n` flag makes sudo fail fast if no sudoers entry is in place
// instead of hanging on a password prompt the daemon can't satisfy.
//
// This is a single best-effort kill. It carries no throttle of its own:
// since 🎯T72.5 the monitor fires it at most once per wedge episode (see
// RunMonitor), and the operator-facing `spyder doctor --fix` is the
// supported path for a deliberate retry. Killing usbmuxd repeatedly while
// it stays wedged was found to be actively harmful — when the wedge is
// device-side it does nothing but churn — so the looping behaviour was
// removed.
//
// Outcomes are reported via slog: Info on success (usbmuxd was killed;
// launchd respawns it), Error on sudo-not-configured or helper failure.
func AttemptRecovery(ctx context.Context) error {
	return attemptRecovery(ctx, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	})
}

// Host tests supply a command executor. They must never invoke privileged USB
// recovery merely to discover whether the developer configured passwordless sudo.
func attemptRecovery(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error)) error {
	out, runErr := run(ctx, "sudo", "-n", recoveryHelperPath)
	trimmed := strings.TrimSpace(string(out))
	if runErr != nil {
		slog.Error("wedge: auto-recovery failed",
			"helper", recoveryHelperPath,
			"error", runErr.Error(),
			"output", trimmed)
		return fmt.Errorf("sudo %s: %w", recoveryHelperPath, runErr)
	}
	slog.Info("wedge: auto-recovery invoked",
		"helper", recoveryHelperPath, "output", trimmed)
	return nil
}
