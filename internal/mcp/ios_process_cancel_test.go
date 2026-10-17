// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios/appservice"
	"github.com/danielpaulus/go-ios/ios/xpc"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/paths"
)

type socketProcessRead struct {
	conn     *xpc.Connection
	returned chan struct{}
}

func (r *socketProcessRead) ListProcesses() ([]appservice.Process, error) {
	defer close(r.returned)
	if err := r.conn.Send(map[string]interface{}{"request": "processes"}); err != nil {
		return nil, err
	}
	_, err := r.conn.ReceiveOnServerClientStream()
	return nil, err
}

type cancellableIOSProbe struct {
	*device.IOSAdapter
	invalidated atomic.Int32
}

func (a *cancellableIOSProbe) InvalidateDevice(string) { a.invalidated.Add(1) }

type emptyProcesses struct{}

func (emptyProcesses) ListProcesses() ([]appservice.Process, error) { return nil, nil }

// Activates the actual IOSAdapter.AppPIDContext, native process retry logic,
// Dispatch cancellation and same-device lifecycle gate. The only substituted
// boundary is hardware channel creation; the stalled read uses go-ios's real
// XPC codec over a duplex socket and begins only after the peer sees a request.
func TestIOSProcessCancellation_RetiresReadAndReleasesLifecycle(t *testing.T) {
	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.WriteFile(paths.InventoryPath(), []byte(testInventory), 0600); err != nil {
		t.Fatal(err)
	}
	c, peer := net.Pipe()
	t.Cleanup(func() { _ = c.Close(); _ = peer.Close() })
	xc, err := xpc.New(c, c, c)
	if err != nil {
		t.Fatal(err)
	}
	received, returned := make(chan struct{}), make(chan struct{})
	var acquisitions, invalidations atomic.Int32
	ios := device.NewIOSProcessProbeAdapter(device.IOSProcessProbe{
		InstalledExecutable: func(_, _ string) (string, error) { return "/Applications/StockCars.app/StockCars", nil },
		MajorVersion:        func(string) (int, error) { return 27, nil },
		AcquireContext: func(ctx context.Context, _ string) (device.IOSProcessListConn, func(), func(), error) {
			if acquisitions.Add(1) > 1 {
				return emptyProcesses{}, func() {}, func() {}, nil
			}
			closed := make(chan struct{})
			stop := context.AfterFunc(ctx, func() { defer close(closed); _ = xc.Close() })
			return &socketProcessRead{xc, returned}, func() {
				if !stop() {
					<-closed
				}
			}, func() {}, nil
		},
		Invalidate: func(string) { invalidations.Add(1) },
	})
	owned := &cancellableIOSProbe{IOSAdapter: ios}
	h := NewHandlerWithAdapters(owned, &stubAdapter{state: func(string) (device.State, error) { return device.State{ForegroundApp: "healthy"}, nil }})
	go func() {
		if _, err := xpc.DecodeMessage(peer); err == nil {
			close(received)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := h.Dispatch(ctx, "terminate_app", map[string]any{"device": "iPad", "bundle_id": "com.example.app"})
		if err != nil || res == nil || !res.IsError {
			t.Errorf("cancel result=%+v err=%v", res, err)
		}
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("real RPC request never reached peer")
	}
	healthy, err := h.Dispatch(context.Background(), "device_state", map[string]any{"device": "Raspberry"})
	if err != nil || healthy.IsError {
		t.Fatalf("cross-device status=%+v %v", healthy, err)
	}
	cancel()
	for _, ch := range []<-chan struct{}{done, returned} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("cancelled handler/read did not finish")
		}
	}
	nextCtx, nextCancel := context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	res, err := h.Dispatch(nextCtx, "terminate_app", map[string]any{"device": "iPad", "bundle_id": "com.example.app"})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("same-device cleanup gate remained blocked: %+v %v", res, err)
	}
	h.invalidateOnTimeout("terminate_app", "iPad")
	if owned.invalidated.Load() != 0 {
		t.Fatal("blanket invalidation can close a replacement channel")
	}
	if invalidations.Load() != 0 {
		t.Fatal("cancelled process read was retried/invalidated as a transport failure")
	}
	if acquisitions.Load() != 3 {
		t.Fatalf("acquisitions=%d, want initial read + not-running termination and verification", acquisitions.Load())
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("test did not activate cancellation")
	}
}
