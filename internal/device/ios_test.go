// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"
	"testing"
)

func TestParseBattery(t *testing.T) {
	valid := []byte(`{
		"AppleRawCurrentCapacity": 5701,
		"AppleRawMaxCapacity": 5982,
		"AppleRawExternalConnected": true
	}`)
	level, charging, err := parseBattery(valid)
	if err != nil {
		t.Fatalf("parseBattery(valid) err = %v", err)
	}
	if level != 95 { // 5701/5982 * 100 = 95.3 truncated
		t.Errorf("level = %d; want 95", level)
	}
	if !charging {
		t.Error("charging = false; want true")
	}

	// Missing max → error.
	_, _, err = parseBattery([]byte(`{"AppleRawCurrentCapacity": 100}`))
	if err == nil {
		t.Error("parseBattery with zero max returned nil err; want error")
	}

	// Malformed JSON → error.
	_, _, err = parseBattery([]byte(`not json`))
	if err == nil {
		t.Error("parseBattery with bad JSON returned nil err; want error")
	}
}

func TestParseIOSPID(t *testing.T) {
	cases := []struct {
		in      string
		wantPID int
		wantErr bool
	}{
		{"1234", 1234, false},
		{" 1234\n", 1234, false},
		{"com.foo.bar: 5678", 5678, false},
		{"com.foo.bar -> 9012", 9012, false},
		{"com.foo.bar:   3456\n", 3456, false},
		{"", 0, true},
		{"not a number", 0, true},
		{"0", 0, true}, // PID <= 0 invalid
		{"-5", 0, true},
	}
	for _, c := range cases {
		pid, err := parseIOSPID([]byte(c.in))
		if c.wantErr {
			if err == nil {
				t.Errorf("parseIOSPID(%q) = %d, nil; want error", c.in, pid)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseIOSPID(%q) err = %v; want nil", c.in, err)
			continue
		}
		if pid != c.wantPID {
			t.Errorf("parseIOSPID(%q) = %d; want %d", c.in, pid, c.wantPID)
		}
	}
}

func TestIsDeviceNotConnected(t *testing.T) {
	yes := []string{
		"NoDeviceConnectedError",
		"ERROR Device not found: 99999-9999",
		"no devices connected",
	}
	for _, s := range yes {
		if !isDeviceNotConnected(s) {
			t.Errorf("isDeviceNotConnected(%q) = false; want true", s)
		}
	}
	no := []string{
		"",
		"unrelated error",
		"couldn't connect to daemon",
	}
	for _, s := range no {
		if isDeviceNotConnected(s) {
			t.Errorf("isDeviceNotConnected(%q) = true; want false", s)
		}
	}
}

func TestIsDeviceLocked(t *testing.T) {
	yes := []string{
		"DvtException: {'BSErrorCodeDescription': 'Locked', ...",
		"Unable to launch com.foo because the device was not,\nor could not be, unlocked.",
		"...the device was not, or could not be, unlocked",
	}
	for _, s := range yes {
		if !isDeviceLocked(s) {
			t.Errorf("isDeviceLocked(%q) = false; want true", s[:min(60, len(s))])
		}
	}
	if isDeviceLocked("BSErrorCodeDescription: 'Security'") {
		t.Error("isDeviceLocked on Security error = true; want false")
	}
}

func TestIsIOSAppNotFound(t *testing.T) {
	if !isIOSAppNotFound("application is not installed") {
		t.Error("isIOSAppNotFound didn't match 'not installed'")
	}
	if !isIOSAppNotFound("bundle com.foo not found") {
		t.Error("isIOSAppNotFound didn't match 'bundle ... not found'")
	}
	if isIOSAppNotFound("some other error") {
		t.Error("isIOSAppNotFound false-positive")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 100); got != "hello" {
		t.Errorf("truncate short = %q; want hello", got)
	}
	got := truncate("hello world here", 5)
	if got != "hello…" {
		t.Errorf("truncate long = %q; want 'hello…'", got)
	}
	if got := truncate("   hello   ", 100); got != "hello" {
		t.Errorf("truncate strips whitespace = %q", got)
	}
}

func TestTailTruncate(t *testing.T) {
	// Input longer than n: keep the tail.
	s := strings.Repeat("a", 100) + "MARKER"
	got := tailTruncate(s, 10)
	if !strings.HasPrefix(got, "…") {
		t.Errorf("tailTruncate should start with …; got %q", got[:min(20, len(got))])
	}
	if !strings.HasSuffix(got, "MARKER") {
		t.Errorf("tailTruncate should preserve tail (MARKER); got %q", got)
	}

	// Short input → unchanged.
	if got := tailTruncate("short", 100); got != "short" {
		t.Errorf("tailTruncate short = %q; want short", got)
	}
}

func TestStringOfAndFirstNonEmpty(t *testing.T) {
	if got := stringOf("hello"); got != "hello" {
		t.Errorf("stringOf string = %q", got)
	}
	if got := stringOf(42); got != "" {
		t.Errorf("stringOf int = %q; want empty", got)
	}
	if got := stringOf(nil); got != "" {
		t.Errorf("stringOf nil = %q", got)
	}
	if got := firstNonEmpty("", "", "third"); got != "third" {
		t.Errorf("firstNonEmpty = %q; want third", got)
	}
	if got := firstNonEmpty("first", "second"); got != "first" {
		t.Errorf("firstNonEmpty first = %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("firstNonEmpty empty = %q", got)
	}
}
