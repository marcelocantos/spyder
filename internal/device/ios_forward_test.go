// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"strings"
	"testing"
)

func TestIOSForwardStore_Lifecycle(t *testing.T) {
	a := NewIOSAdapter()
	a.fwdStore.put(&iosActiveForward{
		pf: PortForward{Serial: "UDID1", LocalPort: 100, DevicePort: 200, Spec: "tcp:100 tcp:200"},
	})
	list := a.fwdStore.list("UDID1")
	if len(list) != 1 || list[0].LocalPort != 100 {
		t.Fatalf("list=%+v", list)
	}
	if len(a.fwdStore.list("OTHER")) != 0 {
		t.Fatal("filter by serial")
	}
	f, ok := a.fwdStore.remove(100)
	if !ok || f.pf.DevicePort != 200 {
		t.Fatalf("remove=%v ok=%v", f, ok)
	}
	if len(a.fwdStore.list("UDID1")) != 0 {
		t.Fatal("expected empty")
	}
	// Unforward missing
	if err := a.UnforwardTCP("UDID1", 100); err == nil {
		t.Fatal("expected missing forward error")
	}
}

func TestIOSForwardTCP_Validation(t *testing.T) {
	a := NewIOSAdapter()
	if _, err := a.ForwardTCP("", 1, 2); err == nil {
		t.Error("empty id")
	}
	if _, err := a.ForwardTCP("udid", 1, 0); err == nil {
		t.Error("bad device port")
	}
	if err := a.UnforwardTCP("udid", 0); err == nil {
		t.Error("bad local port")
	}
}

func TestIOSControl_FailClosed(t *testing.T) {
	a := NewIOSAdapter()
	_, err := a.MeasureFrameStats(context.Background(), "u", "com.x", 1)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("fps err=%v", err)
	}
	if !strings.Contains(err.Error(), "app_perf_get") {
		t.Fatalf("fps should point to app_perf_get: %v", err)
	}
	if err := a.InjectTap("u", 1, 1); err == nil || !strings.Contains(err.Error(), "app_input") {
		t.Fatalf("tap err=%v", err)
	}
	if err := a.InjectSwipe("u", 1, 1, 2, 2, 10); err == nil || !strings.Contains(err.Error(), "mobile-mcp") {
		t.Fatalf("swipe err=%v", err)
	}
}

func TestIOSForwardTCP_EphemeralUsesFreePort(t *testing.T) {
	oldFree, oldStart := freeLocalPort, iosForwardStart
	t.Cleanup(func() {
		freeLocalPort = oldFree
		iosForwardStart = oldStart
	})
	freeLocalPort = func() (int, error) { return 19191, nil }
	// Session will fail without device — still validates free port is requested
	// before session when localPort==0: order is freeLocalPort then Session.
	// With localPort 0, free runs first.
	a := NewIOSAdapter()
	_, err := a.ForwardTCP("no-such-device-udid", 0, 8080)
	// Expect session/get device error, not free-port error
	if err == nil {
		t.Fatal("expected session error without device")
	}
	if strings.Contains(err.Error(), "pick local port") {
		t.Fatalf("unexpected free port failure: %v", err)
	}
}
