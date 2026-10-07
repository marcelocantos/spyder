// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

// TestAppChannelDiagnose_RealDevice is a reproducible host+USB-device smoke.
// It intentionally does NOT send hello: an accepted device dial must remain
// diagnosable as a non-connecting session. adb reverse is temporary and removed.
// Set SPYDER_LIVE_UDID to a connected Android device serial to opt in.
func TestAppChannelDiagnose_RealDevice(t *testing.T) {
	serial := os.Getenv("SPYDER_LIVE_UDID")
	if serial == "" {
		t.Skip("set SPYDER_LIVE_UDID to an Android device serial")
	}
	h := startAppChannelHandler(t)
	key := appchannel.AppKey{DeviceID: serial, BundleID: "com.example.nonconnecting"}
	l, err := h.appChannel.GetOrCreateListener(key)
	if err != nil {
		t.Fatal(err)
	}
	l.RecordInjectedAddress(fmt.Sprintf("127.0.0.1:%d", l.Port)) // device-side adb reverse endpoint
	args := map[string]any{"device_id": serial, "bundle_id": key.BundleID}
	before := diagnoseListener(t, h, args)
	if before["connectivity"].(map[string]any)["accepted_tcp"] != float64(0) {
		t.Fatal(before)
	}
	port := fmt.Sprintf("tcp:%d", l.Port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "adb", "-s", serial, "reverse", port, port)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("adb reverse: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("adb", "-s", serial, "reverse", "--remove", port).Run() })
	cmd = exec.CommandContext(ctx, "adb", "-s", serial, "shell", "sh", "-c", fmt.Sprintf("echo not-hello | nc -w 2 127.0.0.1 %d", l.Port))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("nc closed after bad hello (%v): %s", err, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		after := diagnoseListener(t, h, args)
		c := after["connectivity"].(map[string]any)
		if c["accepted_tcp"] == float64(1) && c["handshake_failures"] == float64(1) && strings.Contains(after["next_check"].(string), "hello handshake") {
			t.Logf("real-device no-session diagnosis: %+v", after)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("device dial not diagnosed: %+v", after)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
