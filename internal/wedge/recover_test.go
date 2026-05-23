// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"testing"
)

// TestAttemptRecovery_Throttles confirms two back-to-back calls
// produce one attempt and one skip. The exec call inside will
// usually fail (no sudoers entry in test environment) but the
// throttle behaviour is independent of that.
func TestAttemptRecovery_Throttles(t *testing.T) {
	resetRecoveryThrottle()
	t.Cleanup(resetRecoveryThrottle)

	ctx := context.Background()

	fired1, _ := AttemptRecovery(ctx)
	if !fired1 {
		t.Fatal("first AttemptRecovery returned fired=false; want true")
	}
	fired2, _ := AttemptRecovery(ctx)
	if fired2 {
		t.Error("second AttemptRecovery within throttle returned fired=true; want false")
	}
}
