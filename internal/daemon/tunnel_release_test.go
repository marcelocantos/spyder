// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/iostunnel"
)

// On 2026-11-24 a graceful restart left both iPhones' runners but
// stopped the tunnel they talk through, and both dropped at once. With
// runners left, the tunnel must outlive shutdown, including the health
// supervisor's stop when its context ends; with none, it is stopped.
func TestFinishTunnel_KeepsTheTunnelForLeftRunners(t *testing.T) {
	for _, left := range []bool{true, false} {
		t.Run(strconv.FormatBool(left), func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("HOME", tmp)
			pidFile := filepath.Join(tmp, "pid")
			bin := filepath.Join(tmp, "ios")
			script := "#!/bin/sh\necho $$ > " + pidFile + "\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/tunnels", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[]")) })
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: mux}
			go func() { _ = srv.Serve(ln) }()
			defer srv.Close()

			sup := iostunnel.New(bin, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
			if err := sup.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			tunnelCtx, stopSupervision := context.WithCancel(context.Background())
			supervised := make(chan struct{})
			go func() {
				health.NewSupervisor(health.New()).Supervise(tunnelCtx, sup, health.Policy{MaxAttempts: 1}, time.Hour)
				close(supervised)
			}()
			var pid int
			deadline := time.Now().Add(2 * time.Second)
			for pid == 0 && time.Now().Before(deadline) {
				b, _ := os.ReadFile(pidFile)
				pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("fake tunnel never started")
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			finishTunnel(ctx, sup, left)
			stopSupervision()
			<-supervised
			time.Sleep(100 * time.Millisecond)

			alive := syscall.Kill(pid, 0) == nil
			if left && !alive {
				t.Fatal("tunnel stopped although runners were left riding on it")
			}
			if !left && alive {
				t.Fatal("tunnel still running after a shutdown that left no runners")
			}
		})
	}
}
