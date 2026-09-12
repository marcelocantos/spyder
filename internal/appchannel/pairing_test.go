// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestListenerBindsLoopbackByDefault(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)
	l, err := m.Start(StartParams{Owner: "test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)

	ip := l.BoundIP()
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("BoundIP() = %v; want loopback (Fable F2: default bind must not be 0.0.0.0)", ip)
	}
	if l.PairingToken != "" {
		t.Fatalf("loopback listener minted pairing token %q; want empty", l.PairingToken)
	}
}

func TestLANBindMintsPairingTokenAndRejectsHelloWithoutIt(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)
	m.SetListenHost("0.0.0.0")
	l, err := m.Start(StartParams{Owner: "test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)

	ip := l.BoundIP()
	if ip == nil || ip.IsLoopback() {
		t.Fatalf("BoundIP() = %v; want all-interfaces", ip)
	}
	if l.PairingToken == "" {
		t.Fatal("LAN bind must mint a pairing token")
	}

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	helloParams, _ := PackParams(Hello{AppName: "intruder", AppVersion: "1"})
	if err := WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	ack, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if ack.Error == nil {
		t.Fatal("Hello without pairing token must be rejected on a LAN bind")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(l.Sessions()) > 0 {
			t.Fatal("rejected Hello must not become a Session")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLANBindAcceptsHelloWithPairingToken(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)
	m.SetListenHost("0.0.0.0")
	l, err := m.Start(StartParams{Owner: "test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)
	if l.PairingToken == "" {
		t.Fatal("LAN bind must mint a pairing token")
	}

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	helloParams, _ := PackParams(Hello{
		AppName:    "paired",
		AppVersion: "1",
		Token:      l.PairingToken,
		Methods:    MethodDescriptors(MethodPing),
	})
	if err := WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	ack, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if ack.Error != nil {
		t.Fatalf("Hello with pairing token rejected: %+v", ack.Error)
	}

	s := waitForSession(t, l)
	if s.HelloInfo() == nil || s.HelloInfo().AppName != "paired" {
		t.Fatalf("HelloInfo = %+v; want paired", s.HelloInfo())
	}
}

func TestGetOrCreateListenerOnWildcardMintsToken(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)
	key := AppKey{DeviceID: "dev", BundleID: "com.example.app"}
	l, err := m.GetOrCreateListenerOn(key, "0.0.0.0")
	if err != nil {
		t.Fatalf("GetOrCreateListenerOn: %v", err)
	}
	t.Cleanup(l.Stop)
	if l.PairingToken == "" {
		t.Fatal("wildcard keyed listener must mint a pairing token")
	}
	if ip := l.BoundIP(); ip == nil || ip.IsLoopback() {
		t.Fatalf("BoundIP() = %v; want all-interfaces", ip)
	}
}
