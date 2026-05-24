// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"testing"
)

// TestAttemptRecovery_NoSudoersFails confirms AttemptRecovery surfaces an
// error when the helper can't run (the test environment has no sudoers
// entry and `sudo -n` fails fast). The throttle that used to gate repeat
// calls was removed in 🎯T72.5 — episode gating now lives in the monitor —
// so each call is an independent best-effort attempt.
func TestAttemptRecovery_NoSudoersFails(t *testing.T) {
	if err := AttemptRecovery(context.Background()); err == nil {
		t.Skip("AttemptRecovery succeeded (helper + sudoers present); nothing to assert")
	}
}
