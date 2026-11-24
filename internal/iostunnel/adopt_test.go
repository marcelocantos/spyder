// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package iostunnel

import (
	"context"
	"net"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeRegistry serves the tunnel registry's /tunnels on a loopback port.
func fakeRegistry(t *testing.T) int {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnels", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[]")) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// fakeProcs stubs the process table and signals for adopted pids.
type fakeProcs struct {
	mu      sync.Mutex
	procs   []proc
	alive   map[int]bool
	signals []syscall.Signal
}

func installFakeProcs(t *testing.T, f *fakeProcs) {
	t.Helper()
	oldList, oldAlive, oldSignal, oldGrace := listProcs, pidAlive, signalPID, stopGrace
	listProcs = func() ([]proc, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.procs, nil }
	pidAlive = func(pid int) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.alive[pid] }
	signalPID = func(pid int, sig syscall.Signal) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.signals = append(f.signals, sig)
		if sig == syscall.SIGTERM || sig == syscall.SIGKILL {
			f.alive[pid] = false
		}
		return nil
	}
	stopGrace = 200 * time.Millisecond
	t.Cleanup(func() { listProcs, pidAlive, signalPID, stopGrace = oldList, oldAlive, oldSignal, oldGrace })
}

func tunnelProc(pid, ppid int) proc {
	return proc{PID: pid, PPID: ppid, Args: []string{"/opt/homebrew/Cellar/spyder/0.113.0/libexec/spyder/ios", "tunnel", "start", "--userspace"}}
}

// Only a tunnel whose daemon has exited is taken over; a tunnel with a
// live parent belongs to a running daemon (seen from a :3131 dev daemon).
func TestAdoptOrphan_TakesOnlyAPreviousDaemonsTunnel(t *testing.T) {
	port := fakeRegistry(t)
	cases := []struct {
		name  string
		procs []proc
		want  int
	}{
		{"orphan", []proc{tunnelProc(500, 1), {PID: 700, PPID: 1, Args: []string{"/x/ios", "runwda", "--udid=u"}}}, 500},
		{"live parent", []proc{tunnelProc(600, 42)}, 0},
		{"two orphans", []proc{tunnelProc(500, 1), tunnelProc(501, 1)}, 0},
		{"none", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			installFakeProcs(t, &fakeProcs{procs: c.procs, alive: map[int]bool{}})
			pid, ok := New("/x/ios", "127.0.0.1", port).AdoptOrphan()
			if ok != (c.want != 0) || pid != c.want {
				t.Fatalf("AdoptOrphan() = %d, %v; want %d", pid, ok, c.want)
			}
		})
	}
}

// Without an answering registry there is nothing healthy to adopt.
func TestAdoptOrphan_NeedsTheRegistry(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	installFakeProcs(t, &fakeProcs{procs: []proc{tunnelProc(500, 1)}, alive: map[int]bool{500: true}})
	if _, ok := New("/x/ios", "127.0.0.1", port).AdoptOrphan(); ok {
		t.Fatal("adopted a tunnel whose registry does not answer")
	}
}

// A healthy adopted tunnel is kept by Start (the health supervisor's
// first call), kept by Stop after Release, and ended by Stop otherwise.
func TestAdoptedTunnel_Lifecycle(t *testing.T) {
	port := fakeRegistry(t)
	f := &fakeProcs{procs: []proc{tunnelProc(500, 1)}, alive: map[int]bool{500: true}}
	installFakeProcs(t, f)
	s := New("/nonexistent/ios", "127.0.0.1", port)
	if _, ok := s.AdoptOrphan(); !ok {
		t.Fatal("did not adopt")
	}
	if !s.Alive() {
		t.Fatal("adopted tunnel not alive")
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start replaced a healthy adopted tunnel: %v", err)
	}
	if len(f.signals) != 0 {
		t.Fatalf("Start signalled the adopted tunnel: %v", f.signals)
	}

	s.Release()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.signals) != 0 || !f.alive[500] {
		t.Fatalf("Stop after Release ended the tunnel: signals %v", f.signals)
	}

	s2 := New("/nonexistent/ios", "127.0.0.1", port)
	if _, ok := s2.AdoptOrphan(); !ok {
		t.Fatal("did not adopt")
	}
	if err := s2.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.signals) == 0 || f.signals[0] != syscall.SIGTERM || f.alive[500] {
		t.Fatalf("Stop did not end the adopted tunnel: signals %v", f.signals)
	}
}
