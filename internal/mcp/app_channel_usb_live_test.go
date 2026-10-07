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
	"github.com/marcelocantos/spyder/internal/device"
)

// TestUSBAndroidAppChannel_Live is opt-in: supply a USB adb serial with an
// installed, debuggable, appchannel-enabled app. It proves a *real app* dials
// back over the USB reverse mapping and answers an RPC, not just a socket
// probe. The caller owns the device; the test force-stops the app afterward.
func TestUSBAndroidAppChannel_Live(t *testing.T) { liveAndroidAppChannel(t, false) }

// TestAndroidLANException_Live verifies an explicit serial exception on a
// device whose app UID cannot use adb reverse. Requires both the live-device
// env and SPYDER_APP_CHANNEL_ANDROID_LAN_SERIALS naming that device.
func TestAndroidLANException_Live(t *testing.T) {
	if os.Getenv("SPYDER_LIVE_ANDROID_LAN_EXCEPTION") != "1" {
		t.Skip("opt in with SPYDER_LIVE_ANDROID_LAN_EXCEPTION=1")
	}
	liveAndroidAppChannel(t, true)
}

func liveAndroidAppChannel(t *testing.T, expectLAN bool) {
	serial := os.Getenv("SPYDER_LIVE_ANDROID_SERIAL")
	bundle := os.Getenv("SPYDER_LIVE_ANDROID_BUNDLE")
	if serial == "" || bundle == "" {
		t.Skip("set SPYDER_LIVE_ANDROID_SERIAL and SPYDER_LIVE_ANDROID_BUNDLE")
	}
	if strings.Contains(serial, ":") || strings.HasPrefix(serial, "emulator-") {
		t.Fatal("requires a USB-attached physical Android serial")
	}
	h := &Handler{appChannel: appchannel.NewManager()}
	defer h.appChannel.Close()
	ad := device.NewAndroidAdapter()
	env, err := h.ensureAppChannelEnv(nil, "android", serial, bundle)
	if err != nil {
		t.Fatal(err)
	}
	key := appchannel.AppKey{DeviceID: serial, BundleID: bundle}
	l, ok := h.appChannel.LookupKeyed(key)
	if !ok {
		t.Fatal("keyed listener missing")
	}
	defer l.Stop()
	if expectLAN {
		if strings.HasPrefix(env["SPYDER_APP_CHANNEL"], "127.0.0.1:") {
			t.Fatalf("LAN exception did not select LAN: %s", env["SPYDER_APP_CHANNEL"])
		}
	} else {
		if want := fmt.Sprintf("127.0.0.1:%d", l.Port); env["SPYDER_APP_CHANNEL"] != want {
			t.Fatalf("channel=%s want %s", env["SPYDER_APP_CHANNEL"], want)
		}
		assertNotLANReachable(t, l.Port)
		if out, err := exec.Command("adb", "-s", serial, "reverse", "--list").CombinedOutput(); err == nil {
			t.Logf("reverse list: %s", out)
		}
	}
	if err := ad.LaunchApp(serial, bundle, env); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ad.TerminateApp(serial, bundle); err != nil {
			t.Logf("terminate: %v", err)
		}
	}()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range h.appChannel.Sessions() {
			if hello := s.HelloInfo(); hello != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, err := s.Call(ctx, appchannel.MethodPing, nil, 3*time.Second)
				cancel()
				if err != nil {
					t.Fatalf("app hello %s but ping failed: %v", hello.AppName, err)
				}
				t.Logf("appchannel handshake and ping: app=%s addr=%s", hello.AppName, env["SPYDER_APP_CHANNEL"])
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no app channel handshake from %s after launch; addr=%s connectivity=%+v", bundle, env["SPYDER_APP_CHANNEL"], l.Connectivity())
}
