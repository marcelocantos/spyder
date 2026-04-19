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

func TestParseUsbmuxList(t *testing.T) {
	data := []byte(`[
		{
			"UniqueDeviceID": "00008103-000D39301A6A201E",
			"DeviceName": "Pippa",
			"DeviceClass": "iPad",
			"ProductType": "iPad13,16",
			"ProductVersion": "26.3.1"
		},
		{
			"UniqueDeviceID": "00008110-0014182E0AC2801E",
			"DeviceName": "Minicades Test iPhone",
			"ProductType": "iPhone14,5",
			"ProductVersion": "26.2"
		}
	]`)
	got, err := parseUsbmuxList(data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d devices; want 2", len(got))
	}
	if got[0].UUID != "00008103-000D39301A6A201E" || got[0].Name != "Pippa" ||
		got[0].Model != "iPad13,16" || got[0].OS != "iOS 26.3.1" || got[0].Platform != "ios" {
		t.Errorf("device 0: %+v", got[0])
	}
	if got[1].Name != "Minicades Test iPhone" {
		t.Errorf("device 1 Name = %q", got[1].Name)
	}
}

func TestParseUsbmuxList_Empty(t *testing.T) {
	got, err := parseUsbmuxList([]byte(`[]`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d; want 0", len(got))
	}
}

func TestParseUsbmuxList_BadJSON(t *testing.T) {
	_, err := parseUsbmuxList([]byte(`not json`))
	if err == nil {
		t.Error("want error on bad JSON")
	}
}

func TestParseDevicectlList(t *testing.T) {
	data := []byte(`{
		"info": {"outcome": "success"},
		"result": {
			"devices": [
				{
					"identifier": "E1A01EA6-8D77-556C-B18D-D470B2909E87",
					"hardwareProperties": {
						"udid": "00008103-000D39301A6A201E",
						"marketingName": "iPad Air (5th generation)",
						"productType": "iPad13,16"
					},
					"deviceProperties": {
						"name": "Pippa",
						"osVersionNumber": "26.3.1"
					}
				},
				{
					"identifier": "CD2E3380-F1AB-5D03-BBA8-E5A68ADB3261",
					"hardwareProperties": {
						"udid": "00008110-0014182E0AC2801E",
						"marketingName": "iPhone 13"
					},
					"deviceProperties": {
						"name": "Minicades Test iPhone",
						"osVersionNumber": "26.2"
					}
				}
			]
		}
	}`)
	got, err := parseDevicectlList(data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d; want 2", len(got))
	}
	if got[0].UUID != "00008103-000D39301A6A201E" {
		t.Errorf("UDID preferred over CoreDevice UUID: %q", got[0].UUID)
	}
	if got[0].Model != "iPad Air (5th generation)" {
		t.Errorf("Model = %q; want marketingName", got[0].Model)
	}
	if got[0].OS != "iOS 26.3.1" {
		t.Errorf("OS = %q", got[0].OS)
	}
}

func TestParseDevicectlList_MarketingNameFallback(t *testing.T) {
	// When marketingName is absent, productType is used as Model.
	data := []byte(`{"result": {"devices": [{"hardwareProperties": {"udid": "XXXX", "productType": "iPad16,1"}, "deviceProperties": {"name": "Foo"}}]}}`)
	got, err := parseDevicectlList(data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got[0].Model != "iPad16,1" {
		t.Errorf("Model = %q; want iPad16,1 fallback", got[0].Model)
	}
}

func TestParseDevicectlList_UDIDFallbackToIdentifier(t *testing.T) {
	// When hardwareProperties.udid is absent, fall back to the
	// CoreDevice identifier (at least we have *some* stable key).
	data := []byte(`{"result": {"devices": [{"identifier": "CORE-UUID-HERE", "deviceProperties": {"name": "X"}}]}}`)
	got, _ := parseDevicectlList(data)
	if got[0].UUID != "CORE-UUID-HERE" {
		t.Errorf("UUID fallback = %q; want CORE-UUID-HERE", got[0].UUID)
	}
}

func TestMergeIOSDevices_OverlayByUDID(t *testing.T) {
	base := []Info{
		{UUID: "A", Name: "pm3-name", Model: "iPad13,16", OS: "iOS 26.3.1", Platform: "ios"},
		{UUID: "B", Name: "only-in-usbmux", Model: "iPhone14,5", Platform: "ios"},
	}
	overlay := []Info{
		{UUID: "A", Name: "Pippa", Model: "iPad Air (5th generation)", OS: "iOS 26.3.1", Platform: "ios"},
		{UUID: "C", Name: "only-in-devicectl", Model: "iPad mini (A17 Pro)", Platform: "ios"},
	}
	got := mergeIOSDevices(base, overlay)
	if len(got) != 3 {
		t.Fatalf("got %d; want 3", len(got))
	}
	// A: overlay wins on Name + Model (richer fields).
	for _, d := range got {
		switch d.UUID {
		case "A":
			if d.Name != "Pippa" || d.Model != "iPad Air (5th generation)" {
				t.Errorf("A not upgraded: %+v", d)
			}
		case "B":
			if d.Name != "only-in-usbmux" {
				t.Errorf("B lost: %+v", d)
			}
		case "C":
			if d.Name != "only-in-devicectl" {
				t.Errorf("C lost: %+v", d)
			}
		}
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
