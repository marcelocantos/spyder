// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package listenpreflight

import (
	"net"
	"testing"
)

func TestAnyTCPListener_hook(t *testing.T) {
	prev := Hook
	defer func() { Hook = prev }()

	Hook = func(port int) (bool, error) {
		if port != 3030 {
			t.Fatalf("port=%d", port)
		}
		return true, nil
	}
	inUse, err := AnyTCPListener(DaemonPort)
	if err != nil || !inUse {
		t.Fatalf("hook: inUse=%v err=%v", inUse, err)
	}

	Hook = func(port int) (bool, error) { return false, nil }
	inUse, err = AnyTCPListener(DaemonPort)
	if err != nil || inUse {
		t.Fatalf("hook clear: inUse=%v err=%v", inUse, err)
	}
}

func TestAnyTCPListener_detectsListener(t *testing.T) {
	Hook = nil
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	inUse, err := AnyTCPListener(port)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !inUse {
		t.Fatalf("expected listener on %d to be detected", port)
	}
}

func TestAnyTCPListener_freePort(t *testing.T) {
	Hook = nil
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	inUse, err := AnyTCPListener(port)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if inUse {
		t.Fatalf("expected no listener on closed port %d", port)
	}
}
