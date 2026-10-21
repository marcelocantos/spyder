// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/runs"
)

// versionAdapter is a device.Adapter that only answers the 🎯T137
// provenance probes; any other call panics via the nil embed.
type versionAdapter struct {
	device.Adapter
	osCalls, appCalls int
}

func (v *versionAdapter) OSVersion(_ context.Context, id string) (device.OSVersion, error) {
	v.osCalls++
	return device.OSVersion{Version: "14", Build: "UP1A.231005.007"}, nil
}

func (v *versionAdapter) AppVersion(_ context.Context, id, bundle string) (device.AppVersion, error) {
	v.appCalls++
	if bundle != "com.example.scr" {
		return device.AppVersion{}, device.ErrAppNotInstalled
	}
	return device.AppVersion{Version: "2.4.1", Build: "241"}, nil
}

func TestReserveRelease_RecordsRunProvenance(t *testing.T) {
	fake := &versionAdapter{}
	h, _, store := newHandlerWithRuns(t, nil, fake)
	h.noProvenanceProbe = false

	res := dispatchJSON(t, h, "reserve", map[string]any{
		"device": "Raspberry", "owner": "stock-car",
		"script_path":   "bundled:skeleton",
		"app_bundle_id": "com.example.scr", "app_name": "Stock Car Racing",
		"app_commit": "c0ffee", "app_branch": "main", "app_dirty": false,
	})
	if res.IsError {
		t.Fatalf("reserve: %s", resultText(t, &res))
	}
	run, err := store.Active("Raspberry", "stock-car")
	if err != nil || run == nil {
		t.Fatalf("no active run: %v", err)
	}
	v := run.Versions
	if v.App.Name != "Stock Car Racing" || v.App.Version != "2.4.1" || v.App.Build != "241" ||
		v.App.Commit != "c0ffee" || v.App.Branch != "main" || v.App.Dirty != "false" {
		t.Errorf("app = %+v", v.App)
	}
	if v.DeviceOS.Version != "14" || v.DeviceOS.Build != "UP1A.231005.007" {
		t.Errorf("device_os = %+v", v.DeviceOS)
	}
	if v.Script.Path != "bundled:skeleton" || v.Script.Source != "bundled" {
		t.Errorf("script = %+v", v.Script)
	}
	if fake.osCalls != 1 || fake.appCalls != 1 {
		t.Errorf("probe calls os=%d app=%d", fake.osCalls, fake.appCalls)
	}

	rel := dispatchJSON(t, h, "release", map[string]any{
		"device": "Raspberry", "owner": "stock-car",
		"passed": false, "outcome": "lap timer stuck",
		"failure_screenshots": []any{"shot-0042.png"},
	})
	if rel.IsError {
		t.Fatalf("release: %s", resultText(t, &rel))
	}
	got, _ := store.Get(run.ID)
	if got.ClosedAt == nil || got.Result == nil {
		t.Fatalf("run not closed with result: %+v", got)
	}
	r := got.Result
	if r.Passed != "false" || r.Outcome != "lap timer stuck" || !r.FailureScreenshots.Known ||
		len(r.FailureScreenshots.Names) != 1 || r.FailureScreenshots.Names[0] != "shot-0042.png" {
		t.Errorf("result = %+v", r)
	}
}

func TestReserveRelease_NoProvenanceArgsRecordsUnknown(t *testing.T) {
	h, _, store := newHandlerWithRuns(t, nil, nil)
	_ = dispatchJSON(t, h, "reserve", map[string]any{"device": "iPad", "owner": "o"})
	run, _ := store.Active("iPad", "o")
	if run == nil {
		t.Fatal("no run")
	}
	_ = dispatchJSON(t, h, "release", map[string]any{"device": "iPad", "owner": "o"})
	got, _ := store.Get(run.ID)
	if got.Versions.App != (runs.AppVersion{Name: runs.Unknown, BundleID: runs.Unknown, Version: runs.Unknown, Build: runs.Unknown, Commit: runs.Unknown, Branch: runs.Unknown, Dirty: runs.Unknown}) {
		t.Errorf("app = %+v", got.Versions.App)
	}
	if got.Versions.DeviceOS.Version != runs.Unknown || got.Result.Passed != runs.Unknown || got.Result.FailureScreenshots.Known {
		t.Errorf("versions=%+v result=%+v", got.Versions, got.Result)
	}
}

func TestRelease_RejectsNonStringFailureScreenshots(t *testing.T) {
	h, _, _ := newHandlerWithRuns(t, nil, nil)
	_ = dispatchJSON(t, h, "reserve", map[string]any{"device": "iPad", "owner": "o"})
	rel := dispatchJSON(t, h, "release", map[string]any{"device": "iPad", "owner": "o", "failure_screenshots": []any{1.0}})
	if !rel.IsError {
		t.Fatal("want error for non-string failure_screenshots")
	}
}
