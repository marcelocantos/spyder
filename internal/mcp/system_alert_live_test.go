// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 🎯T154 live: a fresh install of an app that uses the local network raises
// iOS's Local Network alert; spyder reads it and taps Allow without anyone
// at the device. Needs the alert runner installed (make alert-runner) and
// UI automation allowed on the device.
//
//	SPYDER_LIVE_ALERT_DEVICE=Jevons SPYDER_LIVE_ALERT_APP=/path/StockCars.app \
//	SPYDER_LIVE_ALERT_BUNDLE=com.minicades.stockcars go test -run TestSystemAlertLive ./internal/mcp/
func TestSystemAlertLive(t *testing.T) {
	dev, app, bundle := os.Getenv("SPYDER_LIVE_ALERT_DEVICE"), os.Getenv("SPYDER_LIVE_ALERT_APP"), os.Getenv("SPYDER_LIVE_ALERT_BUNDLE")
	if dev == "" || app == "" || bundle == "" {
		t.Skip("set SPYDER_LIVE_ALERT_DEVICE, SPYDER_LIVE_ALERT_APP and SPYDER_LIVE_ALERT_BUNDLE (an app that asks for Local Network access)")
	}
	h := NewHandler()
	_ = dispatchJSON(t, h, "uninstall_app", map[string]any{"device": dev, "bundle_id": bundle})
	dispatchJSONMap(t, h, "deploy_app", map[string]any{"device": dev, "path": app, "bundle_id": bundle})

	var alert map[string]any
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		read := dispatchJSONMap(t, h, "system_alert", map[string]any{"device": dev})
		alert, _ = read["alert"].(map[string]any)
		if alert["showing"] == true {
			break
		}
		time.Sleep(time.Second)
	}
	t.Logf("alert: %v", alert)
	if alert["showing"] != true || alert["owner"] != "SpringBoard" || !strings.Contains(strings.ToLower(alert["title"].(string)), "local network") {
		t.Fatalf("no Local Network alert after a fresh install: %v", alert)
	}
	if r := dispatchJSON(t, h, "system_alert_tap", map[string]any{"device": dev, "button": "Ask Later"}); !r.IsError || !strings.Contains(resultText(t, &r), "has no button") {
		t.Fatalf("a missing button must fail clearly: %s", resultText(t, &r))
	}
	tap := dispatchJSONMap(t, h, "system_alert_tap", map[string]any{"device": dev, "button": "Allow", "wait_ms": 10000.0})
	after, _ := tap["after"].(map[string]any)
	t.Logf("after tap: %v", after)
	if title, _ := after["title"].(string); after["showing"] == true && strings.Contains(strings.ToLower(title), "local network") {
		t.Fatalf("Local Network alert still showing after Allow: %v", tap)
	}
}
