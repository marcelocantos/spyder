// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"net"
	"strconv"
	"testing"
	"time"
)

func TestProbeConnectivity_MissingDialBack(t *testing.T) {
	saveMissing, saveWindow := ConnectivityMissingDialBack, ConnectivityLaunchWindow
	ConnectivityMissingDialBack = 10 * time.Millisecond
	ConnectivityLaunchWindow = time.Minute
	t.Cleanup(func() {
		ConnectivityMissingDialBack = saveMissing
		ConnectivityLaunchWindow = saveWindow
	})

	m := NewManager()
	key := AppKey{DeviceID: "00008130-001122334455667A", BundleID: "com.example.app"}
	l, err := m.GetOrCreateListener(key)
	if err != nil {
		t.Fatal(err)
	}
	launched := time.Now().Add(-30 * time.Second)
	launches := map[AppKey]time.Time{key: launched}
	_ = l
	time.Sleep(20 * time.Millisecond) // age listener lastTouched from creation

	got := ProbeConnectivity(m, launches, time.Now())
	if got.MissingDialBacks != 1 {
		t.Fatalf("MissingDialBacks = %d; want 1", got.MissingDialBacks)
	}
}

func TestProbeConnectivity_PingFailure(t *testing.T) {
	saveSilence, saveTimeout := ConnectivityPingSilence, ConnectivityPingTimeout
	ConnectivityPingSilence = 0
	ConnectivityPingTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		ConnectivityPingSilence = saveSilence
		ConnectivityPingTimeout = saveTimeout
	})

	m := NewManager()
	key := AppKey{DeviceID: "00008130-001122334455667B", BundleID: "com.example.app2"}
	l, err := m.GetOrCreateListener(key)
	if err != nil {
		t.Fatal(err)
	}

	// App connects but does not respond to ping.
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	helloParams, _ := PackParams(Hello{
		AppName: "silent",
		Methods: MethodDescriptors(MethodPing),
	})
	if err := WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: helloParams}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFrame(conn); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(l.Sessions()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(l.Sessions()) == 0 {
		t.Fatal("expected accepted session")
	}
	l.Sessions()[0].lastProgress.Store(time.Now().Add(-time.Minute).UnixNano())

	got := ProbeConnectivity(m, nil, time.Now())
	if got.PingFailures != 1 {
		t.Fatalf("PingFailures = %d; want 1", got.PingFailures)
	}
}

func TestConnectivityReport_StressPresent(t *testing.T) {
	empty := ConnectivityReport{}
	if empty.StressPresent() {
		t.Fatal("empty report should not stress")
	}
	withPing := ConnectivityReport{PingFailures: 1}
	if !withPing.StressPresent() {
		t.Fatal("ping failure should stress")
	}
}
