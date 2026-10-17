// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	ios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/appservice"
	"github.com/danielpaulus/go-ios/ios/coredevice"
	"github.com/danielpaulus/go-ios/ios/xpc"
	"github.com/marcelocantos/spyder/internal/goios"
	"golang.org/x/net/http2"
)

type socketResolver struct{}

func (socketResolver) Session(string) (ios.DeviceEntry, error) { return ios.DeviceEntry{}, nil }

type generationRead struct{ conn *xpc.Connection }

func (c *generationRead) ListProcesses() ([]appservice.Process, error) {
	if err := c.conn.Send(map[string]interface{}{"query": "processes"}); err != nil {
		return nil, err
	}
	_, err := c.conn.ReceiveOnServerClientStream()
	return nil, err
}
func TestIOSAdapter_LateReadErrorInvalidationPreservesReplacement(t *testing.T) {
	peers := make(chan net.Conn, 4)
	pool := goios.NewServicePool(socketResolver{}, func(ios.DeviceEntry) (*xpc.Connection, error) {
		client, server := net.Pipe()
		peers <- server
		return xpc.New(client, client, client)
	}, func(c *xpc.Connection) error { return c.Close() }, time.Minute)
	defer pool.Close()
	invalidating, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	unblock := func() { resumeOnce.Do(func() { close(resume) }) }
	defer unblock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapter := NewIOSProcessProbeAdapter(IOSProcessProbe{
		InstalledExecutable: func(_, _ string) (string, error) { return "/Applications/Fixture.app/Fixture", nil }, MajorVersion: func(string) (int, error) { return 27, nil },
		AcquireContext: func(ctx context.Context, id string) (IOSProcessListConn, func(), func(), error) {
			lease, err := pool.AcquireLease(ctx, id)
			if err != nil {
				return nil, func() {}, func() {}, err
			}
			return &generationRead{lease.Conn}, lease.Release, func() { close(invalidating); <-resume; lease.Invalidate() }, nil
		},
	})
	done := make(chan error, 1)
	go func() { _, err := adapter.AppPIDContext(ctx, "dut", "com.example.fixture"); done <- err }()
	peer := <-peers
	go func() { _, _ = xpc.DecodeMessage(peer); _ = peer.Close() }()
	select {
	case <-invalidating:
	case <-time.After(time.Second):
		t.Fatal("actual read error did not reach adapter invalidation")
	}
	// Cancellation occurs after the retry helper's ctx check. A blanket UDID
	// invalidator delayed here would evict the newly installed connection.
	cancel()
	pool.Invalidate("dut")
	current, release, err := pool.Acquire("dut")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	replacementPeer := <-peers
	defer replacementPeer.Close()
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled adapter did not return")
	}
	received := make(chan error, 1)
	go func() { _, err := xpc.DecodeMessage(replacementPeer); received <- err }()
	if err := current.Send(map[string]interface{}{"replacement": "healthy"}); err != nil {
		t.Fatalf("adapter invalidated replacement: %v", err)
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
	if pool.Opens() != 2 {
		t.Fatalf("cancelled adapter retried: opens=%d", pool.Opens())
	}
}

func TestForwardedProcessRequest_RequiresCompleteActualCodecMessage(t *testing.T) {
	var message bytes.Buffer
	body := coredevice.BuildRequest("fixture-device", "com.apple.coredevice.feature.listprocesses", nil)
	if err := xpc.EncodeMessage(&message, xpc.Message{Body: body}); err != nil {
		t.Fatal(err)
	}
	raw := message.Bytes()
	var frames bytes.Buffer
	framer := http2.NewFramer(&frames, nil)
	if err := framer.WriteData(1, false, raw[:len(raw)-1]); err != nil {
		t.Fatal(err)
	}
	if completeForwardedProcessRequest(frames.Bytes()) {
		t.Fatal("partial XPC message counted as forwarded RPC")
	}
	if err := framer.WriteData(1, false, raw[len(raw)-1:]); err != nil {
		t.Fatal(err)
	}
	if !completeForwardedProcessRequest(frames.Bytes()) {
		t.Fatal("complete actual XPC message not observed")
	}
	if completeForwardedProcessRequest(frames.Bytes()[:frames.Len()-1]) {
		t.Fatal("partial HTTP2 DATA counted as complete RPC")
	}
}
