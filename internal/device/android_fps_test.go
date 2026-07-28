// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

const sampleGfxInfo = `Applications Graphics Acceleration Info:

** Graphics info for pid 1234 [com.example.app] **

Stats since: 123456789 ns
Total frames rendered: 300
Janky frames: 15 (5.00%)
50th percentile: 8ms
90th percentile: 12ms
95th percentile: 16ms
99th percentile: 24ms
Number Missed Vsync: 2
`

func TestParseGfxInfo(t *testing.T) {
	total, janky, pct, err := ParseGfxInfo(sampleGfxInfo)
	if err != nil {
		t.Fatal(err)
	}
	if total != 300 {
		t.Errorf("total=%d want 300", total)
	}
	if janky != 15 {
		t.Errorf("janky=%d want 15", janky)
	}
	if pct < 4.9 || pct > 5.1 {
		t.Errorf("pct=%v want ~5", pct)
	}
}

func TestParseGfxInfo_MissingTotal(t *testing.T) {
	_, _, _, err := ParseGfxInfo("no stats here")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMeasureFrameStats_CommandBoundary(t *testing.T) {
	if _, err := exec.LookPath("adb"); err != nil {
		t.Skip("adb not in PATH")
	}
	var calls [][]string
	oldAdb, oldSleep := androidAdb, androidSleep
	t.Cleanup(func() {
		androidAdb = oldAdb
		androidSleep = oldSleep
	})
	androidSleep = func(d time.Duration) {
		if d != 2*time.Second {
			t.Errorf("sleep=%v want 2s", d)
		}
	}
	androidAdb = func(args ...string) (stdout, stderr []byte, err error) {
		cp := append([]string{}, args...)
		calls = append(calls, cp)
		if len(args) >= 6 && args[len(args)-1] == "reset" {
			return []byte(""), nil, nil
		}
		return []byte(sampleGfxInfo), nil, nil
	}

	a := NewAndroidAdapter()
	st, err := a.MeasureFrameStats("SERIAL", "com.example.app", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalFrames != 300 {
		t.Errorf("total=%d", st.TotalFrames)
	}
	if st.FPS < 149 || st.FPS > 151 {
		t.Errorf("fps=%v want ~150", st.FPS)
	}
	if st.Source != "gfxinfo" {
		t.Errorf("source=%q", st.Source)
	}
	if len(calls) < 2 {
		t.Fatalf("adb calls=%d want >=2", len(calls))
	}
	first := strings.Join(calls[0], " ")
	if !strings.Contains(first, "reset") {
		t.Errorf("first call should reset: %v", calls[0])
	}
	if calls[0][1] != "SERIAL" {
		t.Errorf("serial=%v", calls[0])
	}
}

func TestMeasureFrameStats_Validation(t *testing.T) {
	a := NewAndroidAdapter()
	if _, err := a.MeasureFrameStats("", "pkg", time.Second); err == nil {
		t.Error("empty id")
	}
	if _, err := a.MeasureFrameStats("s", "", time.Second); err == nil {
		t.Error("empty package")
	}
	if _, err := a.MeasureFrameStats("s", "pkg", 0); err == nil {
		t.Error("zero window")
	}
}
