// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
)

func TestAppExec_WaitStateDeviceSettingAsserts(t *testing.T) {
	h := startAppChannelHandler(t)
	h.android = &stubAdapter{
		setSystemSetting: func(id, key, value string) (device.SettingResult, error) {
			return device.SettingResult{Key: key, Action: "set", Value: value}, nil
		},
		restoreSystemSetting: func(id, key string) (device.SettingResult, error) {
			return device.SettingResult{Key: key, Action: "restore"}, nil
		},
	}
	_, port := openListener(t, h)
	conn := dialSequencedSliceApp(t, port, "carousel", []map[string]any{
		{"present": false, "active": false},
		{"present": true, "active": true, "tick": 7},
	})
	defer conn.Close()
	_ = waitForAppSession(t, h)

	res, err := h.handleAppExec(map[string]any{
		"script": `
c = wait_state(slice="carousel", select="select(.present == true and .active == true)", timeout_ms=2000, poll_ms=10)
emit(c)
s = device_setting(device="Raspberry", key="refresh_rate", value="60")
emit(s)
r = device_setting(device="Raspberry", key="refresh_rate", restore=True)
emit(r)
assert_settle(awake=[True, True, False], max_steps=5)
emit("asserts_ok")
`,
		"max_duration_ms": 8000.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("app_exec: %v", texts(res))
	}
	joined := strings.Join(texts(res), "\n")
	if !strings.Contains(joined, "asserts_ok") {
		t.Errorf("missing asserts_ok: %s", joined)
	}
	if !strings.Contains(joined, "refresh_rate") {
		t.Errorf("missing settings result: %s", joined)
	}
	if !strings.Contains(joined, "present") && !strings.Contains(joined, "tick") {
		t.Errorf("missing wait value: %s", joined)
	}
}
