// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSettingAllowlist_UnknownNeverTouchesShell(t *testing.T) {
	var calls int
	old := androidAdb
	t.Cleanup(func() { androidAdb = old })
	androidAdb = func(args ...string) (stdout, stderr []byte, err error) {
		calls++
		t.Errorf("adb invoked for unknown key: %v", args)
		return nil, nil, nil
	}

	a := NewAndroidAdapter()
	_, err := a.SetSystemSetting("SERIAL", "animation_scale", "0")
	if err == nil {
		t.Fatal("expected unknown key rejected")
	}
	if !strings.Contains(err.Error(), "allowlist") || !strings.Contains(err.Error(), "animation_scale") {
		t.Errorf("want allowlist rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("unknown key reached adb %d time(s)", calls)
	}

	_, err = a.RestoreSystemSetting("SERIAL", "secure/foo")
	if err == nil || calls != 0 {
		t.Fatalf("restore unknown: err=%v calls=%d", err, calls)
	}
}

func TestAndroidSetting_SetThenRestoreDeletesPin(t *testing.T) {
	if _, err := exec.LookPath("adb"); err != nil {
		t.Skip("adb not in PATH")
	}
	store := map[string]string{
		"peak_refresh_rate": "null",
		"min_refresh_rate":  "null",
	}
	var ops []string
	old := androidAdb
	t.Cleanup(func() { androidAdb = old })
	androidAdb = func(args ...string) (stdout, stderr []byte, err error) {
		i := indexOf(args, "settings")
		if i < 0 || i+2 >= len(args) {
			t.Fatalf("unexpected adb args: %v", args)
		}
		op := args[i+1]
		name := args[i+3]
		ops = append(ops, op+" "+name)
		switch op {
		case "get":
			return []byte(store[name] + "\n"), nil, nil
		case "put":
			store[name] = args[i+4]
			return nil, nil, nil
		case "delete":
			store[name] = "null"
			return nil, nil, nil
		default:
			t.Fatalf("op %s", op)
		}
		return nil, nil, nil
	}

	a := NewAndroidAdapter()
	set, err := a.SetSystemSetting("SERIAL", SettingRefreshRate, "60")
	if err != nil {
		t.Fatal(err)
	}
	if set.Action != "set" || set.Key != SettingRefreshRate {
		t.Errorf("set result = %+v", set)
	}
	if store["peak_refresh_rate"] != "60" || store["min_refresh_rate"] != "60" {
		t.Errorf("after set store=%v", store)
	}

	rest, err := a.RestoreSystemSetting("SERIAL", SettingRefreshRate)
	if err != nil {
		t.Fatal(err)
	}
	if rest.Action != "restore" {
		t.Errorf("action=%s", rest.Action)
	}
	if store["peak_refresh_rate"] != "null" || store["min_refresh_rate"] != "null" {
		t.Fatalf("restore left pin in place: %v", store)
	}
	joined := strings.Join(ops, ";")
	if !strings.Contains(joined, "delete peak_refresh_rate") || !strings.Contains(joined, "delete min_refresh_rate") {
		t.Errorf("restore must delete both names; ops=%v", ops)
	}
	if strings.Contains(joined, "put peak_refresh_rate null") {
		t.Error("restore must delete, not put a sentinel")
	}
}

func TestIOSSetting_FailClosed(t *testing.T) {
	ios := NewIOSAdapter()
	_, err := ios.SetSystemSetting("UDID", SettingRefreshRate, "60")
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("ios set: %v", err)
	}
	_, err = ios.RestoreSystemSetting("UDID", SettingRefreshRate)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("ios restore: %v", err)
	}
	// Unknown key is rejected before the platform message.
	_, err = ios.SetSystemSetting("UDID", "nope", "1")
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("ios unknown: %v", err)
	}
	if strings.Contains(err.Error(), "not supported") && !strings.Contains(err.Error(), "allowlist") {
		t.Error("unknown key should not look like a platform limitation")
	}
}

func indexOf(ss []string, w string) int {
	for i, s := range ss {
		if s == w {
			return i
		}
	}
	return -1
}
