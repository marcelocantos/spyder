// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func TestUSBAndroidAppChannelReverseHandshake(t *testing.T) {
	oldReverse, oldRemove := reverseAppChannel, unreverseAppChannel
	t.Cleanup(func() { reverseAppChannel, unreverseAppChannel = oldReverse, oldRemove })
	var mapped, removed int
	reverseAppChannel = func(id string, port int) error {
		if id != "SERIAL" {
			t.Fatalf("id=%s", id)
		}
		mapped = port
		return nil
	}
	unreverseAppChannel = func(id string, port int) error { removed = port; return nil }
	h := newHandlerWithStubs(t, nil, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	env, err := h.ensureAppChannelEnv(nil, "android", "SERIAL", "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	l, ok := h.appChannel.LookupKeyed(appchannel.AppKey{DeviceID: "SERIAL", BundleID: "com.example.app"})
	if !ok || mapped != l.Port {
		t.Fatalf("missing mapping: listener=%v mapped=%d", ok, mapped)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", mapped); env["SPYDER_APP_CHANNEL"] != want {
		t.Fatalf("injection=%s want %s", env["SPYDER_APP_CHANNEL"], want)
	}
	assertNotLANReachable(t, mapped)
	c := dialSmoke(t, l.Port, []string{appchannel.MethodPing})
	defer c.close()
	_ = waitForAppSession(t, h)
	_, err = h.ensureAppChannelEnv(nil, "android", "SERIAL", "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	l.Stop()
	if removed != mapped {
		t.Fatalf("reverse mapping not removed: got %d want %d", removed, mapped)
	}
}

func assertNotLANReachable(t *testing.T, port int) {
	t.Helper()
	// The listener's bound address is exposed by dialing: checking a private
	// interface IP must fail even on a host where no firewall is configured.
	hosts, err := appchannelLANHosts()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range hosts {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 100000000)
		if err == nil {
			conn.Close()
			t.Fatalf("LAN host %s can dial loopback-only port %d", host, port)
		}
	}
}

func TestUSBAndroidReverseFailureNeverFallsBackToLAN(t *testing.T) {
	old := reverseAppChannel
	t.Cleanup(func() { reverseAppChannel = old })
	reverseAppChannel = func(string, int) error { return errors.New("USB unavailable") }
	h := newHandlerWithStubs(t, nil, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	_, err := h.ensureAppChannelEnv(nil, "android", "SERIAL", "com.example.app")
	if err == nil || !strings.Contains(err.Error(), "USB unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestUSBAndroidLaunchInstallsReverseBeforeApp(t *testing.T) {
	old := reverseAppChannel
	t.Cleanup(func() { reverseAppChannel = old })
	mapped := 0
	reverseAppChannel = func(id string, port int) error { mapped = port; return nil }
	var injected string
	android := &stubAdapter{launchApp: func(id, bundle string, env map[string]string) error {
		injected = env["SPYDER_APP_CHANNEL"]
		if mapped == 0 {
			t.Fatal("launch preceded reverse")
		}
		if injected != fmt.Sprintf("127.0.0.1:%d", mapped) {
			t.Fatalf("env=%s", injected)
		}
		return nil
	}}
	h := newHandlerWithStubs(t, nil, android)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	r := dispatchJSON(t, h, "launch_app", map[string]any{"device": "Raspberry", "bundle_id": "com.example.app"})
	if r.IsError {
		t.Fatalf("launch: %s", resultText(t, &r))
	}
	assertNotLANReachable(t, mapped)
	c := dialSmoke(t, mapped, []string{appchannel.MethodPing})
	defer c.close()
	_ = waitForAppSession(t, h)
}

func TestPhysicalIOSAppChannelIsLANBound(t *testing.T) {
	t.Setenv("SPYDER_APP_CHANNEL_HOST", "192.168.1.217")
	h := newHandlerWithStubs(t, nil, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	env, err := h.ensureAppChannelEnv(nil, "ios", "00008103-001122334455667A", "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	l, ok := h.appChannel.LookupKeyed(appchannel.AppKey{DeviceID: "00008103-001122334455667A", BundleID: "com.example.app"})
	if !ok || env["SPYDER_APP_CHANNEL"] != fmt.Sprintf("192.168.1.217:%d", l.Port) {
		t.Fatalf("env=%v listener=%v", env, ok)
	}
	hosts, err := appchannelLANHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) == 0 {
		t.Skip("no LAN interface available")
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", hosts[0], l.Port))
	if err != nil {
		t.Fatalf("iOS listener not reachable on LAN interface: %v", err)
	}
	conn.Close()
}

func TestAndroidLANExceptionIsPerSerial(t *testing.T) {
	t.Setenv("SPYDER_APP_CHANNEL_ANDROID_LAN_SERIALS", " A23, OTHER ")
	t.Setenv("SPYDER_APP_CHANNEL_HOST", "192.168.1.217")
	old := reverseAppChannel
	t.Cleanup(func() { reverseAppChannel = old })
	mapped := 0
	reverseAppChannel = func(_ string, _ int) error { mapped++; return nil }
	h := newHandlerWithStubs(t, nil, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	lan, err := h.ensureAppChannelEnv(nil, "android", "A23", "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(lan["SPYDER_APP_CHANNEL"], "192.168.1.217:") || mapped != 0 {
		t.Fatalf("LAN exception=%v reverse=%d", lan, mapped)
	}
	usb, err := h.ensureAppChannelEnv(nil, "android", "USB_GOOD", "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(usb["SPYDER_APP_CHANNEL"], "127.0.0.1:") || mapped != 1 {
		t.Fatalf("USB unaffected=%v reverse=%d", usb, mapped)
	}
}
