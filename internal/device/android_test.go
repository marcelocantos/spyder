// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"testing"
)

func TestParseAndroidBattery(t *testing.T) {
	// Typical: USB powered, level 87.
	out := []byte(`Current Battery Service state:
  AC powered: false
  USB powered: true
  Wireless powered: false
  Dock powered: false
  status: 2
  level: 87
  scale: 100
  voltage: 4322
  temperature: 352
`)
	level, charging, err := parseAndroidBattery(out)
	if err != nil {
		t.Fatalf("parseAndroidBattery err = %v", err)
	}
	if level != 87 {
		t.Errorf("level = %d; want 87", level)
	}
	if !charging {
		t.Error("charging = false; want true (USB powered = true)")
	}

	// Not charging (emulator or unplugged).
	out2 := []byte(`  AC powered: false
  USB powered: false
  Wireless powered: false
  Dock powered: false
  level: 100
`)
	_, charging, err = parseAndroidBattery(out2)
	if err != nil {
		t.Fatalf("parseAndroidBattery(unplugged) err = %v", err)
	}
	if charging {
		t.Error("charging = true on fully-unplugged; want false")
	}

	// Wireless powered = charging.
	out3 := []byte(`  Wireless powered: true
  level: 50
`)
	_, charging, err = parseAndroidBattery(out3)
	if err != nil {
		t.Fatalf("parseAndroidBattery(wireless) err = %v", err)
	}
	if !charging {
		t.Error("charging = false on wireless-powered; want true")
	}

	// No level field → error.
	_, _, err = parseAndroidBattery([]byte(`AC powered: true`))
	if err == nil {
		t.Error("parseAndroidBattery without level returned nil; want error")
	}
}

func TestForegroundAppRegex(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"  mFocusedApp=ActivityRecord{ffaf61 u0 com.squz.tiltbuggy/.TiltBuggyActivity t42104}", "com.squz.tiltbuggy"},
		{"    mResumedActivity: ActivityRecord{abc123 u0 com.android.settings/.Settings t99}", "com.android.settings"},
		{"some unrelated line", ""},
	}
	for _, c := range cases {
		m := fgActivityRE.FindStringSubmatch(c.line)
		var got string
		if len(m) >= 2 {
			got = m[1]
		}
		if got != c.want {
			t.Errorf("fgActivityRE on %q = %q; want %q", c.line, got, c.want)
		}
	}
}

func TestIsAndroidDeviceNotConnected(t *testing.T) {
	yes := []string{
		"error: device 'R5CR112X76K' not found",
		"error: device offline",
		"error: device unauthorized",
		"no devices/emulators found",
		"error: device 'nonexistent-serial' not found\n",
	}
	for _, s := range yes {
		if !isAndroidDeviceNotConnected(s) {
			t.Errorf("isAndroidDeviceNotConnected(%q) = false; want true", s)
		}
	}
	no := []string{
		"",
		"some unrelated adb error",
		"adb server is out of date",
	}
	for _, s := range no {
		if isAndroidDeviceNotConnected(s) {
			t.Errorf("isAndroidDeviceNotConnected(%q) = true; want false", s)
		}
	}
}
