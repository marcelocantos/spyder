// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
)

func TestDeviceSetting_AndroidSetRestore(t *testing.T) {
	var setKey, setVal, restKey string
	android := &stubAdapter{
		setSystemSetting: func(id, key, value string) (device.SettingResult, error) {
			setKey, setVal = key, value
			return device.SettingResult{
				Key:    key,
				Action: "set",
				Value:  value,
				Names:  []string{"system/peak_refresh_rate", "system/min_refresh_rate"},
				Current: map[string]string{
					"peak_refresh_rate": value,
					"min_refresh_rate":  value,
				},
			}, nil
		},
		restoreSystemSetting: func(id, key string) (device.SettingResult, error) {
			restKey = key
			return device.SettingResult{
				Key:    key,
				Action: "restore",
				Names:  []string{"system/peak_refresh_rate", "system/min_refresh_rate"},
				Current: map[string]string{
					"peak_refresh_rate": "null",
					"min_refresh_rate":  "null",
				},
			}, nil
		},
	}
	h := newHandlerWithStubs(t, nil, android)
	r := dispatchJSON(t, h, "device_setting", map[string]any{
		"device": "Raspberry", "key": "refresh_rate", "value": "60",
	})
	if r.IsError {
		t.Fatalf("set: %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if setKey != "refresh_rate" || setVal != "60" {
		t.Errorf("set called key=%q val=%q", setKey, setVal)
	}
	if !strings.Contains(body, "refresh_rate") || !strings.Contains(body, `"set"`) {
		t.Errorf("set body=%s", body)
	}

	r2 := dispatchJSON(t, h, "device_setting", map[string]any{
		"device": "Raspberry", "key": "refresh_rate", "restore": true,
	})
	if r2.IsError {
		t.Fatalf("restore: %s", resultText(t, &r2))
	}
	if restKey != "refresh_rate" {
		t.Errorf("restore key=%q", restKey)
	}
	if !strings.Contains(resultText(t, &r2), "restore") {
		t.Errorf("restore body=%s", resultText(t, &r2))
	}
}

func TestDeviceSetting_UnknownKeyRejectedByAdapter(t *testing.T) {
	android := &stubAdapter{
		setSystemSetting: func(id, key, value string) (device.SettingResult, error) {
			return device.SettingResult{}, device.UnknownSettingError(key)
		},
	}
	h := newHandlerWithStubs(t, nil, android)
	r := dispatchJSON(t, h, "device_setting", map[string]any{
		"device": "Raspberry", "key": "secure/foo", "value": "1",
	})
	if !r.IsError {
		t.Fatal("expected unknown key error")
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "allowlist") {
		t.Errorf("body=%s", body)
	}
}

func TestDeviceSetting_IOSFailClosed(t *testing.T) {
	ios := device.NewIOSAdapter()
	h := newHandlerWithStubs(t, ios, nil)
	r := dispatchJSON(t, h, "device_setting", map[string]any{
		"device": "iPad", "key": "refresh_rate", "value": "60",
	})
	if !r.IsError {
		t.Fatal("expected iOS fail-closed")
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "not supported") {
		t.Errorf("want not supported, got %s", body)
	}
}

func TestT111ToolsDispatchKnown_IncludesDeviceSetting(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Dispatch(t.Context(), "device_setting", map[string]any{
		"device": "x", "key": "refresh_rate",
	})
	if err != nil && strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown tool: %v", err)
	}
}
