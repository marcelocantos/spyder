// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestAndroidPower_Live reads power and lock state from real devices
// (read-only dumpsys). SPYDER_LIVE_ANDROID_SERIALS is a comma-separated
// serial list.
func TestAndroidPower_Live(t *testing.T) {
	serials := os.Getenv("SPYDER_LIVE_ANDROID_SERIALS")
	if serials == "" {
		t.Skip("set SPYDER_LIVE_ANDROID_SERIALS to run")
	}
	a := NewAndroidAdapter()
	for _, s := range strings.Split(serials, ",") {
		st, err := a.State(s)
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		if st.Power == nil || st.Power.Health == "" || st.Power.Health == PowerUnknown {
			t.Errorf("%s: no power health: %+v notes=%v", s, st.Power, st.Notes)
			continue
		}
		if st.Power.KeyguardShowing == nil || st.Power.CredentialSet == nil || st.Power.Wakefulness == "" {
			t.Errorf("%s: lock/screen fields missing: %+v notes=%v", s, st.Power, st.Notes)
		}
		b, _ := json.Marshal(st.Power)
		t.Logf("%s %s", s, b)
	}
}
