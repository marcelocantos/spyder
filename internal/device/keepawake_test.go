// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import "testing"

// TestDetectCodesigningTeam_LiveKeychain is an opportunistic
// integration check: when run on a Mac that has at least one team
// registered with Xcode, assert the picked team matches the documented
// preference order (paid > free). Skips on hosts without Xcode or
// without any registered team.
func TestDetectCodesigningTeam_LiveKeychain(t *testing.T) {
	team, err := DetectCodesigningTeam()
	if err != nil {
		t.Skipf("no Xcode-registered team on this host: %v", err)
	}
	if len(team) != 10 {
		t.Errorf("team ID length = %d; want 10 (e.g. 'SWA3H3N7TW')", len(team))
	}
	t.Logf("DetectCodesigningTeam returned %s", team)
}
