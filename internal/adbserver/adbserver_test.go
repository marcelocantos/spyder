// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package adbserver

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeADB writes a marker each time it is run and exits 0.
func fakeADB(t *testing.T) (bin, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "starts")
	bin = filepath.Join(dir, "adb")
	script := "#!/bin/sh\necho \"$@\" >> " + marker + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, marker
}

// The server closing the track-devices connection is a restart: the
// supervisor runs start-server and reports it with evidence.
func TestSupervisor_RestartsWhenServerGoesAway(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	bin, marker := fakeADB(t)
	events := make(chan Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go New(Args{
		Adb: bin, Addr: ln.Addr().String(),
		OnRestart: func(e Event) { events <- e },
		Evidence:  func() string { return "4242 adb kill-server <- Unity" },
	}).Run(ctx)

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	req := make([]byte, 4+len("host:track-devices"))
	if _, err := io.ReadFull(conn, req); err != nil || string(req) != "0012host:track-devices" {
		t.Fatalf("request %q %v", req, err)
	}
	if _, err := conn.Write([]byte("OKAY0000")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	_ = conn.Close() // the server exits

	select {
	case e := <-events:
		if e.Err != nil || e.Up.IsZero() || !strings.Contains(e.Evidence, "kill-server") {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no restart event")
	}
	data, _ := os.ReadFile(marker)
	if !strings.Contains(string(data), "start-server") {
		t.Fatalf("start-server not run: %q", data)
	}
	// It reconnects to the restarted server.
	if _, err := ln.Accept(); err != nil {
		t.Fatal(err)
	}
}

// A server that is down at startup is started without an event.
func TestSupervisor_StartsQuietlyWhenDownAtStartup(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listening
	bin, marker := fakeADB(t)
	events := make(chan Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go New(Args{Adb: bin, Addr: addr, OnRestart: func(e Event) { events <- e }, Evidence: func() string { return "" }}).Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, _ := os.ReadFile(marker); strings.Contains(string(data), "start-server") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if data, _ := os.ReadFile(marker); !strings.Contains(string(data), "start-server") {
		t.Fatal("start-server not run")
	}
	select {
	case e := <-events:
		t.Fatalf("unexpected event %+v", e)
	default:
	}
}

func TestPickEvidence(t *testing.T) {
	ps := `  1     0 10-00:00:00 /sbin/launchd
  500   1 01:00 /Applications/Unity/Hub/Editor/6000.4.1f1/Unity.app/Contents/MacOS/Unity -batchmode
  501 500 00:01 /Applications/Unity/Hub/Editor/6000.4.1f1/PlaybackEngines/AndroidPlayer/SDK/platform-tools/adb kill-server
  600   1 00:10 /usr/bin/python3 other.py
`
	got := pickEvidence(ps)
	if !strings.Contains(got, "501 ") || !strings.Contains(got, "kill-server <- ") || strings.Contains(got, "python3") {
		t.Fatalf("%s", got)
	}
}
