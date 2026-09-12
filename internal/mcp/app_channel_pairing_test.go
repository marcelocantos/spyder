// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

// Unique-session fallback must not hand the control plane to a peer
// that did not arrive on a keyed launch listener (Fable F2 / invariant #5).
func TestRequireSession_RejectsUnkeyedUniqueFallback(t *testing.T) {
	h := startAppChannelHandler(t)
	l, err := h.appChannel.Start(appchannel.StartParams{Owner: "orphan"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)
	client := dialSmoke(t, l.Port, []string{appchannel.MethodPing})
	defer client.close()
	_ = waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_ping", nil)
	if !r.IsError {
		t.Fatal("app_ping without session_id must not route to an unkeyed peer")
	}
	if !strings.Contains(resultText(t, &r), "session_id") {
		t.Errorf("error should demand session_id or device+bundle_id; got %s", resultText(t, &r))
	}
}

func TestEnsureAppChannelEnv_PhysicalDeviceBindsWildcardAndMintsToken(t *testing.T) {
	orig := lanHosts
	lanHosts = func() ([]string, error) { return []string{"192.0.2.10"}, nil }
	t.Cleanup(func() { lanHosts = orig })

	var got map[string]string
	ios := &stubAdapter{launchApp: func(id, bundle string, env map[string]string) error {
		got = env
		return nil
	}}
	h := newHandlerWithStubs(t, ios, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)

	r := dispatchJSON(t, h, "launch_app", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.foo.bar",
	})
	if r.IsError {
		t.Fatalf("launch_app should succeed; body=%s", resultText(t, &r))
	}
	key := appchannel.AppKey{DeviceID: "00008103-001122334455667A", BundleID: "com.foo.bar"}
	l, ok := h.appChannel.LookupKeyed(key)
	if !ok {
		t.Fatal("no keyed listener after launch_app")
	}
	if ip := l.BoundIP(); ip == nil || ip.IsLoopback() {
		t.Fatalf("physical-device listener BoundIP() = %v; want all-interfaces", ip)
	}
	if l.PairingToken == "" {
		t.Fatal("physical-device LAN bind must mint a pairing token")
	}
	if got["SPYDER_APP_CHANNEL"] != "192.0.2.10:"+strconv.Itoa(l.Port) {
		t.Errorf("SPYDER_APP_CHANNEL = %q; want 192.0.2.10:%d", got["SPYDER_APP_CHANNEL"], l.Port)
	}
	if got["SPYDER_APP_CHANNEL_TOKEN"] != l.PairingToken {
		t.Errorf("SPYDER_APP_CHANNEL_TOKEN = %q; want %q", got["SPYDER_APP_CHANNEL_TOKEN"], l.PairingToken)
	}

	// T128 residue: physical iOS/Android still bind 0.0.0.0 (usbmux has
	// no reverse path; smoke docs keep LAN dial-back). The security
	// floor is loopback-or-token-required — a wildcard bind without a
	// pairing token is the Fable F2 hole. Hello without the minted
	// token must not become a Session; Hello with it must.
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(l.Port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	helloParams, _ := appchannel.PackParams(appchannel.Hello{AppName: "intruder", AppVersion: "1"})
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	ack, err := appchannel.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if ack.Error == nil {
		t.Fatal("physical wildcard Hello without pairing token must be rejected")
	}

	client := dialSmokeHello(t, l.Port, appchannel.Hello{
		AppName:    "paired",
		AppVersion: "1",
		Token:      got["SPYDER_APP_CHANNEL_TOKEN"],
		Methods:    appchannel.MethodDescriptors(appchannel.MethodPing),
	})
	defer client.close()
	_ = waitForAppSession(t, h)
}
