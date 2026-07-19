// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/wedge"
)

// TestHealthReport_IncludesDoctorAndInFlight is the 🎯T99.5 / T99.6 oracle:
// Handler.HealthReport exposes doctor_finding + in_flight alongside entities.
func TestHealthReport_IncludesDoctorAndInFlight(t *testing.T) {
	h := NewHandler()
	wedge.RecordDoctorFinding(true, "test_wedge")
	t.Cleanup(func() { wedge.RecordDoctorFinding(false, "") })

	h.ops.begin("screenshot", "Jevons")
	rep := h.HealthReport()
	if !rep.DoctorFinding.Wedged || rep.DoctorFinding.Detail != "test_wedge" {
		t.Fatalf("doctor_finding: %+v", rep.DoctorFinding)
	}
	if len(rep.InFlight) != 1 || rep.InFlight[0].Tool != "screenshot" || rep.InFlight[0].Device != "Jevons" {
		t.Fatalf("in_flight: %+v", rep.InFlight)
	}
	if rep.InFlight[0].ElapsedMs < 0 {
		t.Fatal("elapsed_ms")
	}
}

// TestForceStallAndDumpHook covers 🎯T99.3 ForceStall + BeforeExit dump path.
func TestForceStallAndDumpHook(t *testing.T) {
	h := NewHandler()
	h.EnableSelfHeal(50*time.Millisecond, 10*time.Millisecond)
	dumps := 0
	exits := 0
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits++ })
	h.selfRestart.SetBeforeExit(func(string) { dumps++ })

	h.dispatchWatch.Begin()
	if !h.dispatchWatch.ForceStall("test stall") {
		t.Fatal("ForceStall should succeed while outstanding")
	}
	snap := h.Health().Model().Snapshot()
	found := false
	for _, e := range snap.Entities {
		if e.ID.Name == "spyder" && string(e.State) == "needs_attention" {
			found = true
		}
	}
	if !found {
		t.Fatalf("spyder entity not needs_attention: %+v", snap.Entities)
	}
	h.selfRestart.Request("test")
	if dumps != 1 || exits != 1 {
		t.Fatalf("dumps=%d exits=%d", dumps, exits)
	}
}
