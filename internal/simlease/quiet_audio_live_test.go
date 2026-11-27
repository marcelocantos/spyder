// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestBootLeavesCallServicesStopped_Live boots a throwaway iOS
// simulator through the lease manager's real Boot and checks that
// callservicesd, which keeps the host's audio output busy (🎯T241), is
// not running in it. Set SPYDER_LIVE_SIM_RUNTIME to a runtime name such
// as "iOS 18.6" to run it.
func TestBootLeavesCallServicesStopped_Live(t *testing.T) {
	runtime := os.Getenv("SPYDER_LIVE_SIM_RUNTIME")
	if runtime == "" {
		t.Skip("SPYDER_LIVE_SIM_RUNTIME not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sim := XcrunSimctl{}
	runtimes, err := sim.Runtimes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := ""
	for _, r := range runtimes {
		if r.Name == runtime {
			runtimeID = r.ID
		}
	}
	if runtimeID == "" {
		t.Fatalf("runtime %q not installed", runtime)
	}
	udid, err := sim.Create(ctx, "spyder-T241-live", "com.apple.CoreSimulator.SimDeviceType.iPhone-16", runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sim.Shutdown(context.Background(), udid)
		_ = sim.Delete(context.Background(), udid)
	})

	if err := sim.Boot(ctx, udid); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "spawn", udid, "launchctl", "list").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "com.apple.telephonyutilities.callservicesd") {
		t.Fatalf("callservicesd is loaded after Boot:\n%s", out)
	}
}
