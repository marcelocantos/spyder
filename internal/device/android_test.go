// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
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
	got, err := parseAndroidBattery(out)
	if err != nil {
		t.Fatalf("parseAndroidBattery err = %v", err)
	}
	if got.Level != 87 {
		t.Errorf("level = %d; want 87", got.Level)
	}
	if !got.Charging {
		t.Error("charging = false; want true (USB powered = true)")
	}

	// Not charging (emulator or unplugged).
	out2 := []byte(`  AC powered: false
  USB powered: false
  Wireless powered: false
  Dock powered: false
  level: 100
`)
	got, err = parseAndroidBattery(out2)
	if err != nil {
		t.Fatalf("parseAndroidBattery(unplugged) err = %v", err)
	}
	if got.Charging {
		t.Error("charging = true on fully-unplugged; want false")
	}

	// Wireless powered = charging.
	out3 := []byte(`  Wireless powered: true
  level: 50
`)
	got, err = parseAndroidBattery(out3)
	if err != nil {
		t.Fatalf("parseAndroidBattery(wireless) err = %v", err)
	}
	if !got.Charging {
		t.Error("charging = false on wireless-powered; want true")
	}

	// No level field → error.
	if _, err = parseAndroidBattery([]byte(`AC powered: true`)); err == nil {
		t.Error("parseAndroidBattery without level returned nil; want error")
	}
}

func TestParseAndroidBattery_GreedyDump(t *testing.T) {
	out := []byte(`Current Battery Service state:
  AC powered: false
  USB powered: true
  Max charging current: 1500000
  current now: 203
  voltage: 3769
  temperature: 282
  Charge counter: 467640
  Adaptive Fast Charging Settings: true
  quick charging: 0
  level: 12
`)
	got, err := parseAndroidBattery(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.Level != 12 || !got.Charging {
		t.Fatalf("level/charging = %d %v", got.Level, got.Charging)
	}
	if got.Extra["current now"] != 203 {
		t.Errorf("current now = %v", got.Extra["current now"])
	}
	if got.Extra["Max charging current"] != 1500000 {
		t.Errorf("Max charging current = %v", got.Extra["Max charging current"])
	}
	if got.Extra["Adaptive Fast Charging Settings"] != true {
		t.Errorf("Adaptive Fast Charging Settings = %v", got.Extra["Adaptive Fast Charging Settings"])
	}
	if got.Extra["USB powered"] != true {
		t.Errorf("USB powered = %v", got.Extra["USB powered"])
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

func TestAndroidAppPIDDistinguishesLookupFailure(t *testing.T) {
	app := NewAndroidAdapter()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"transport", errors.New("transport failed"), "transport failed"},
		{"deadline", context.DeadlineExceeded, "deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(SetAndroidAdbForTest(func(context.Context, ...string) ([]byte, []byte, error) {
				return nil, nil, tc.err
			}))
			_, err := app.AppPID("fixture", "com.example")
			if !errors.Is(err, tc.err) || strings.HasPrefix(err.Error(), "app not running") ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AppPID = %v, want %v", err, tc.err)
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		exit := exec.Command("/bin/sh", "-c", "exit 1").Run()
		t.Cleanup(SetAndroidAdbForTest(func(context.Context, ...string) ([]byte, []byte, error) {
			return nil, nil, exit
		}))
		if _, err := app.AppPID("fixture", "com.example"); err == nil || !strings.HasPrefix(err.Error(), "app not running") {
			t.Fatalf("no pidof result = %v, want app not running", err)
		}
	})
}

func TestAndroidInventoryProbesHaveDeadlines(t *testing.T) {
	for _, name := range []string{"List", "State"} {
		t.Run(name, func(t *testing.T) {
			var calls int
			var deadline time.Time
			t.Cleanup(SetAndroidAdbForTest(func(ctx context.Context, args ...string) ([]byte, []byte, error) {
				calls++
				got, ok := ctx.Deadline()
				if !ok {
					t.Errorf("%s command %v has no deadline", name, args)
				}
				if calls > 1 && deadline != got {
					t.Errorf("%s restarted its budget for %v", name, args)
				}
				deadline = got
				switch {
				case args[0] == "devices":
					return []byte("List of devices attached\nfixture device model:Test\n"), nil, nil
				case args[3] == "getprop":
					return []byte("16\n"), nil, nil
				case args[3] == "dumpsys" && args[4] == "battery":
					return []byte("level: 90\n"), nil, nil
				default:
					return nil, nil, nil
				}
			}))
			a := NewAndroidAdapter()
			var err error
			if name == "List" {
				_, err = a.List()
			} else {
				_, err = a.State("fixture")
			}
			if err != nil || calls != 3 {
				t.Fatalf("%s used %d probe commands, error %v; want three successful fake calls", name, calls, err)
			}
		})
	}
}
