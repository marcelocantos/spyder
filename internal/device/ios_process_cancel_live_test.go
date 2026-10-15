// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/appservice"
	"github.com/danielpaulus/go-ios/ios/xpc"
	"github.com/marcelocantos/spyder/internal/goios"
	"golang.org/x/net/http2"
)

// A test-only transparent byte relay to an actual go-ios userspace tunnel.
// It never decodes or synthesizes protocol replies. After warm-up it can hold
// the server response, and proves the real listprocesses request was forwarded
// upstream before the caller is cancelled. Only process listing is performed.
type processReplyRelay struct {
	listener      net.Listener
	upstream      string
	ctx           context.Context
	cancel        context.CancelFunc
	held          atomic.Bool
	forwarded     chan struct{}
	resume        chan struct{}
	forwardedOnce sync.Once
	resumeOnce    sync.Once
	mu            sync.Mutex
	conns         map[net.Conn]bool
	closed        bool
	wg            sync.WaitGroup
}

func newProcessReplyRelay(upstream string) (*processReplyRelay, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &processReplyRelay{listener: listener, upstream: upstream, ctx: ctx, cancel: cancel, forwarded: make(chan struct{}), resume: make(chan struct{}), conns: map[net.Conn]bool{}}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			r.wg.Add(1)
			go r.serve(client)
		}
	}()
	return r, nil
}
func (r *processReplyRelay) serve(client net.Conn) {
	defer r.wg.Done()
	defer client.Close()
	upstream, err := (&net.Dialer{}).DialContext(r.ctx, "tcp", r.upstream)
	if err != nil {
		return
	}
	defer upstream.Close()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.conns[client] = true
	r.conns[upstream] = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.conns, client); delete(r.conns, upstream); r.mu.Unlock() }()
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		defer client.Close()
		defer upstream.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := upstream.Read(buf)
			if n > 0 {
				if r.held.Load() {
					select {
					case <-r.resume:
					case <-r.ctx.Done():
						return
					}
				}
				if written, werr := client.Write(buf[:n]); werr != nil || written != n {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 32*1024)
	var request []byte
	for {
		n, err := client.Read(buf)
		if n > 0 {
			written, werr := upstream.Write(buf[:n])
			if r.held.Load() && written > 0 {
				request = append(request, buf[:written]...)
				if len(request) > 128*1024 {
					break
				}
				if completeForwardedProcessRequest(request) {
					r.forwardedOnce.Do(func() { close(r.forwarded) })
				}
			}
			if werr != nil || written != n {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_ = upstream.Close()
	_ = client.Close()
	// The response goroutine can be deliberately held until resume/Close.
	<-responseDone
}

// Observe complete forwarded HTTP2 DATA/XPC bytes with the actual codecs;
// a matching feature substring in a partial request is not sufficient proof.
func completeForwardedProcessRequest(raw []byte) bool {
	framer := http2.NewFramer(nil, bytes.NewReader(raw))
	var stream bytes.Buffer
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			return false
		}
		if data, ok := frame.(*http2.DataFrame); ok && data.StreamID == 1 {
			stream.Write(data.Data())
			message, err := xpc.DecodeMessage(bytes.NewReader(stream.Bytes()))
			if err == nil && message.Body["CoreDevice.featureIdentifier"] == "com.apple.coredevice.feature.listprocesses" {
				return true
			}
		}
	}
}

func (r *processReplyRelay) releaseReply() {
	r.held.Store(false)
	r.resumeOnce.Do(func() { close(r.resume) })
}
func (r *processReplyRelay) Close() {
	r.cancel()
	_ = r.listener.Close()
	r.releaseReply()
	r.mu.Lock()
	r.closed = true
	for c := range r.conns {
		_ = c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func TestIOSAcquiredProcessReadCancellation_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" || os.Getenv("SPYDER_LIVE_PROCESS_CANCEL") != "1" {
		t.Skip("explicit hardware-bound read-only cancellation gate not enabled")
	}
	a := NewIOSAdapter()
	t.Cleanup(func() { _ = a.ipPool.Close(); _ = a.asPool.Close(); _ = a.ssPool.Close() })
	dev, major, err := a.goios.SessionWithVersion(udid)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Properties.SerialNumber != udid {
		t.Fatalf("resolved hardware identity differs: %q", dev.Properties.SerialNumber)
	}
	if major < 17 || !dev.UserspaceTUN {
		t.Fatal("requires an actual iOS17+ userspace tunnel")
	}
	relay, err := newProcessReplyRelay(net.JoinHostPort(dev.UserspaceTUNHost, strconv.Itoa(dev.UserspaceTUNPort)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relay.Close)
	_, portText, err := net.SplitHostPort(relay.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	_ = a.asPool.Close()
	a.asPool = goios.NewServicePool(a.goios, func(resolved ios.DeviceEntry) (*appservice.Connection, error) {
		if resolved.Properties.SerialNumber != udid {
			return nil, fmt.Errorf("pool identity mismatch")
		}
		resolved.UserspaceTUNHost = "127.0.0.1"
		resolved.UserspaceTUNPort = port
		return appservice.New(resolved)
	}, func(c *appservice.Connection) error { return c.Close() }, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, release, err := a.asPool.AcquireContext(ctx, udid)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := conn.ListProcesses()
	release()
	if err != nil || len(warm) == 0 {
		t.Fatalf("real warm process census failed: count%d err%v", len(warm), err)
	}
	t.Logf("hardware=%s ios_major=%d warm_process_count=%d relay=%s", udid, major, len(warm), relay.listener.Addr())
	relay.held.Store(true)
	readCtx, readCancel := context.WithCancel(context.Background())
	defer readCancel()
	conn, release, err = a.asPool.AcquireContext(readCtx, udid)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	rpcDone := make(chan struct{})
	t.Cleanup(func() {
		readCancel()
		relay.releaseReply()
		select {
		case <-rpcDone:
		case <-time.After(2 * time.Second):
			t.Error("native read goroutine did not retire")
		}
	})
	go func() {
		defer close(rpcDone)
		defer release()
		_, err := conn.ListProcesses()
		result <- err
	}()
	select {
	case <-relay.forwarded:
	case <-time.After(10 * time.Second):
		readCancel()
		relay.releaseReply()
		t.Fatal("actual listprocesses request was not observed after forwarding upstream")
	}
	started := time.Now()
	readCancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("held native read completed despite blocked response")
		}
		t.Logf("actual forwarded native process read cancelled in %s: %v", time.Since(started), err)
	case <-time.After(2 * time.Second):
		relay.releaseReply()
		t.Fatal("cancelled native read retained its handler")
	}
	<-rpcDone
	relay.releaseReply()
	freshCtx, freshCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer freshCancel()
	next, nextRelease, err := a.asPool.AcquireContext(freshCtx, udid)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := next.ListProcesses()
	nextRelease()
	if err != nil || len(fresh) == 0 {
		t.Fatalf("fresh native process query failed: count%d err%v", len(fresh), err)
	}
	if a.asPool.Opens() != 2 {
		t.Fatalf("connection opens=%d, want cancelled generation replaced once", a.asPool.Opens())
	}
	if !errors.Is(readCtx.Err(), context.Canceled) {
		t.Fatal("cancellation was not activated")
	}
	t.Logf("fresh_process_count=%d replacement_opens=%d; no app launch/input performed", len(fresh), a.asPool.Opens())
}
