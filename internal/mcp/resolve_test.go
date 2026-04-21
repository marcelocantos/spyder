// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

const testInventory = `[
  {
    "alias": "Pippa",
    "platform": "ios",
    "ios_uuid": "00008103-000D39301A6A201E",
    "ios_coredevice": "E1A01EA6-8D77-556C-B18D-D470B2909E87"
  },
  {
    "alias": "Raspberry",
    "platform": "android",
    "android_serial": "R5CR112X76K"
  }
]`

// newTestHandler sets HOME to a temp dir containing testInventory
// and returns a Handler backed by it.
func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".spyder")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inventory.json"), []byte(testInventory), 0o600); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	return NewHandler()
}

func TestResolveAdapter_InventoryIOS(t *testing.T) {
	h := newTestHandler(t)
	adp, platform, id, err := h.resolveAdapter("Pippa")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if platform != "ios" {
		t.Errorf("platform = %q; want ios", platform)
	}
	// iOS picks IOSUUID first.
	if id != "00008103-000D39301A6A201E" {
		t.Errorf("id = %q; want iOS hardware UDID", id)
	}
	if adp == nil {
		t.Error("adapter is nil")
	}
}

func TestResolveAdapter_InventoryAndroid(t *testing.T) {
	h := newTestHandler(t)
	_, platform, id, err := h.resolveAdapter("Raspberry")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if platform != "android" {
		t.Errorf("platform = %q; want android", platform)
	}
	if id != "R5CR112X76K" {
		t.Errorf("id = %q; want R5CR112X76K", id)
	}
}

func TestResolveAdapter_RawIOSUDID(t *testing.T) {
	h := newTestHandler(t)
	_, platform, id, err := h.resolveAdapter("ABCDEF12-1234567890ABCDEF") // not in inventory
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if platform != "ios" {
		t.Errorf("platform = %q; want ios", platform)
	}
	if id != "ABCDEF12-1234567890ABCDEF" {
		t.Errorf("id = %q; want echo-back", id)
	}
}

func TestResolveAdapter_RawCoreDeviceUUID(t *testing.T) {
	h := newTestHandler(t)
	_, platform, _, err := h.resolveAdapter("12345678-1234-1234-1234-1234567890AB") // not in inventory
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if platform != "ios" {
		t.Errorf("platform = %q; want ios", platform)
	}
}

func TestResolveAdapter_RawAndroidSerial(t *testing.T) {
	h := newTestHandler(t)
	_, platform, id, err := h.resolveAdapter("completely-unknown-id")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if platform != "android" {
		t.Errorf("platform = %q; want android (fallback)", platform)
	}
	if id != "completely-unknown-id" {
		t.Errorf("id = %q; want echo-back", id)
	}
}

func TestDispatch_UnknownTool(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch("nonexistent_tool", map[string]any{})
	if err == nil {
		t.Error("Dispatch(unknown) returned nil err; want error")
	}
}

func TestDispatch_ResolveMissingName(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch("resolve", map[string]any{})
	if err == nil {
		t.Error("Dispatch(resolve, {}) returned nil err; want error for missing name")
	}
}
