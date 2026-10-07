// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func diagnoseListener(t *testing.T, h *Handler, args map[string]any) map[string]any {
	t.Helper()
	r := dispatchJSON(t, h, "app_channel_diagnose", args)
	if r.IsError {
		t.Fatalf("diagnose: %s", resultText(t, &r))
	}
	var result struct {
		Listeners []map[string]any `json:"listeners"`
	}
	if err := json.Unmarshal([]byte(resultText(t, &r)), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Listeners) != 1 {
		t.Fatalf("want one listener, got %s", resultText(t, &r))
	}
	return result.Listeners[0]
}

func TestAppChannelDiagnose_NoSessionAndFailedHello(t *testing.T) {
	h := startAppChannelHandler(t)
	key := appchannel.AppKey{DeviceID: "desktop-test", BundleID: "com.example.nonconnecting"}
	env, err := h.ensureAppChannelEnv(nil, "desktop", key.DeviceID, key.BundleID)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := h.appChannel.LookupKeyed(key)
	if !ok {
		t.Fatal("no listener")
	}
	args := map[string]any{"device_id": key.DeviceID, "bundle_id": key.BundleID}
	entry := diagnoseListener(t, h, args)
	c := entry["connectivity"].(map[string]any)
	if !strings.HasSuffix(c["listen_address"].(string), fmt.Sprintf(":%d", l.Port)) || (c["bind_family"] != "v4" && c["bind_family"] != "dual") || c["injected_address"] != env["SPYDER_APP_CHANNEL"] || c["accepted_tcp"] != float64(0) {
		t.Fatalf("unexpected listener: %#v", entry)
	}
	if !strings.Contains(entry["next_check"].(string), "reachability") {
		t.Fatalf("next check: %#v", entry)
	}
	// TCP accepted, but no hello: this is not the same as no device dial.
	conn, err := net.Dial("tcp", env["SPYDER_APP_CHANNEL"])
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		entry = diagnoseListener(t, h, args)
		c = entry["connectivity"].(map[string]any)
		if c["handshake_failures"] == float64(1) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no failed handshake observed: %#v", entry)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c["accepted_tcp"] != float64(1) || c["last_remote"] == nil || c["last_attempt"] == nil || !strings.Contains(entry["next_check"].(string), "hello handshake") {
		t.Fatalf("wrong diagnosis after dial: %#v", entry)
	}
}

func TestAppChannelDiagnose_ExplicitOverrideAndNoListener(t *testing.T) {
	h := startAppChannelHandler(t)
	r := dispatchJSON(t, h, "app_channel_diagnose", map[string]any{"device_id": "missing", "bundle_id": "missing"})
	if !strings.Contains(resultText(t, &r), "no keyed listener") {
		t.Fatal(resultText(t, &r))
	}
	key := appchannel.AppKey{DeviceID: "explicit", BundleID: "app"}
	env, err := h.ensureAppChannelEnv(map[string]string{"SPYDER_APP_CHANNEL": "192.0.2.99:9999"}, "desktop", key.DeviceID, key.BundleID)
	if err != nil {
		t.Fatal(err)
	}
	if env["SPYDER_APP_CHANNEL"] != "192.0.2.99:9999" {
		t.Fatal(env)
	}
	c := diagnoseListener(t, h, map[string]any{"device_id": key.DeviceID, "bundle_id": key.BundleID})["connectivity"].(map[string]any)
	if c["injected_address"] != "192.0.2.99:9999" {
		t.Fatalf("override not reported: %#v", c)
	}
}
