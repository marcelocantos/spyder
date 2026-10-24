// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios/appservice"
)

// wedgedProcesses never answers until its lease context ends, like an
// appservice connection that stops responding after a CoreDevice terminate.
type wedgedProcesses struct{ ctx context.Context }

func (w wedgedProcesses) ListProcesses() ([]appservice.Process, error) {
	<-w.ctx.Done()
	return nil, errors.New("use of closed network connection")
}

// 🎯T176: terminate_app's post-terminate pid check stalled for the whole 60s
// tool deadline on a wedged pooled connection. One bounded attempt must give
// way to a fresh connection.
func TestAppPIDContext_StalledListRetriesOnFreshConnection(t *testing.T) {
	prev := iosProcessListAttemptTimeout
	iosProcessListAttemptTimeout = 50 * time.Millisecond
	t.Cleanup(func() { iosProcessListAttemptTimeout = prev })

	calls, retired := 0, 0
	a := NewIOSProcessProbeAdapter(IOSProcessProbe{
		InstalledExecutable: func(string, string) (string, error) {
			return "/var/containers/Bundle/Application/EXPECTED/StockCars.app/StockCars", nil
		},
		MajorVersion: func(string) (int, error) { return 27, nil },
		AcquireContext: func(ctx context.Context, _ string) (IOSProcessListConn, func(), func(), error) {
			calls++
			if calls == 1 {
				return wedgedProcesses{ctx}, func() {}, func() { retired++ }, nil
			}
			return identityProcesses{{Pid: 42, Path: "/var/containers/Bundle/Application/EXPECTED/StockCars.app/StockCars"}}, func() {}, func() {}, nil
		},
		Invalidate: func(string) {},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	pid, err := a.AppPIDContext(ctx, "dut", "com.fixture.stockcars")
	if err != nil || pid != 42 {
		t.Fatalf("pid=%d err=%v; want 42 from the fresh connection", pid, err)
	}
	if calls != 2 || retired != 1 {
		t.Fatalf("calls=%d retired=%d; want one retired wedged connection then a fresh one", calls, retired)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %s; the stall must be bounded by the attempt timeout", elapsed)
	}
}
