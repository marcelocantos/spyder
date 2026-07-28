// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Live smoke for 🎯T111 Android OS control. Gated on SPYDER_LIVE_ANDROID
// or presence of emulator-5554. Not required for CI.

package device

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func liveAndroidSerial(t *testing.T) string {
	t.Helper()
	if s := os.Getenv("SPYDER_LIVE_ANDROID"); s != "" {
		return s
	}
	out, err := exec.Command("adb", "devices").Output()
	if err != nil {
		t.Skip("adb devices failed")
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(line, "\tdevice") && strings.HasPrefix(line, "emulator-") {
			return strings.Fields(line)[0]
		}
	}
	t.Skip("no emulator device; set SPYDER_LIVE_ANDROID=<serial>")
	return ""
}

func TestT111_AndroidControl_Live(t *testing.T) {
	serial := liveAndroidSerial(t)
	a := NewAndroidAdapter()

	// Port forward lifecycle
	fw, err := a.ForwardTCP(serial, 0, 19999)
	if err != nil {
		t.Fatalf("ForwardTCP: %v", err)
	}
	t.Logf("forward local=%d device=%d", fw.LocalPort, fw.DevicePort)
	list, err := a.ListForwards(serial)
	if err != nil {
		t.Fatalf("ListForwards: %v", err)
	}
	found := false
	for _, f := range list {
		if f.LocalPort == fw.LocalPort {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("forward not listed: %+v", list)
	}
	if err := a.UnforwardTCP(serial, fw.LocalPort); err != nil {
		t.Fatalf("UnforwardTCP: %v", err)
	}

	// Minimal inject
	if err := a.InjectTap(serial, 50, 50); err != nil {
		t.Fatalf("InjectTap: %v", err)
	}
	if err := a.InjectSwipe(serial, 50, 200, 50, 50, 100); err != nil {
		t.Fatalf("InjectSwipe: %v", err)
	}

	// Short FPS window — systemui is usually present
	st, err := a.MeasureFrameStats(context.Background(), serial, "com.android.systemui", time.Second)
	if err != nil {
		// Some builds hide gfxinfo for system packages; still log
		t.Logf("MeasureFrameStats (may be package-limited): %v", err)
	} else {
		t.Logf("fps=%v frames=%d", st.FPS, st.TotalFrames)
		if st.Source != "gfxinfo" {
			t.Errorf("source=%q", st.Source)
		}
	}
}
