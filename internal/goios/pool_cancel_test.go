// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/xpc"
)

func socketPool(t *testing.T) (*ServicePool[*xpc.Connection], <-chan net.Conn) {
	t.Helper()
	r := New(DefaultTunnelHost, DefaultTunnelPort)
	for _, id := range []string{"one", "two"} {
		r.cache[id] = &entry{dev: ios.DeviceEntry{}, major: 27, resolved: time.Now()}
	}
	peers := make(chan net.Conn, 256)
	p := NewServicePool(r, func(ios.DeviceEntry) (*xpc.Connection, error) {
		c, s := net.Pipe()
		peers <- s
		return xpc.New(c, c, c)
	}, func(c *xpc.Connection) error { return c.Close() }, time.Minute)
	t.Cleanup(func() {
		_ = p.Close()
		close(peers)
		for c := range peers {
			_ = c.Close()
		}
	})
	return p, peers
}

func awaitPool(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("socket/lease goroutine did not exit")
	}
}

// Uses actual go-ios XPC encoding/decoding over a real duplex socket. The
// peer accepts a request, then deliberately never replies. Cancellation must
// close the underlying transport and retire the read goroutine, not just
// return a caller-visible deadline while hidden work remains alive.
func TestServicePool_CancelClosesEstablishedXPCRead(t *testing.T) {
	p, peers := socketPool(t)
	for range 10 {
		ctx, cancel := context.WithCancel(context.Background())
		c, release, err := p.AcquireContext(ctx, "one")
		if err != nil {
			t.Fatal(err)
		}
		peer := <-peers
		received, serverDone, readDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(serverDone)
			defer peer.Close()
			_, err := xpc.DecodeMessage(peer)
			if err != nil {
				return
			}
			close(received)
			_, _ = io.Copy(io.Discard, peer)
		}()
		go func() {
			defer close(readDone)
			defer release()
			if err := c.Send(map[string]interface{}{"request": "processes"}); err != nil {
				return
			}
			_, _ = c.ReceiveOnServerClientStream()
		}()
		awaitPool(t, received)
		cancel()
		awaitPool(t, readDone)
		awaitPool(t, serverDone)
		p.mu.Lock()
		_, cached := p.entries["one"]
		p.mu.Unlock()
		if cached {
			t.Fatal("cancelled connection still cached")
		}
	}
	if p.Opens() != 10 {
		t.Fatalf("opens=%d", p.Opens())
	}
}

type observedPoolWait struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedPoolWait) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestServicePool_CancelledWaiterDoesNotCloseOwner(t *testing.T) {
	p, peers := socketPool(t)
	c, release, err := p.Acquire("one")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	peer := <-peers
	defer peer.Close()
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedPoolWait{Context: base, observed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, err := p.AcquireContext(ctx, "one")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("wait error=%v", err)
		}
	}()
	awaitPool(t, ctx.observed)
	cancel()
	awaitPool(t, done)
	received := make(chan struct{})
	go func() {
		defer close(received)
		_, err := xpc.DecodeMessage(peer)
		if err != nil {
			t.Errorf("owner closed by waiter: %v", err)
		}
	}()
	if err := c.Send(map[string]interface{}{"still": "owned"}); err != nil {
		t.Fatal(err)
	}
	awaitPool(t, received)
	// A different device is independent while this owner's permit stays held.
	_, releaseTwo, err := p.Acquire("two")
	if err != nil {
		t.Fatal(err)
	}
	releaseTwo()
}

func TestServicePool_LateOldCancellationCannotEvictReplacement(t *testing.T) {
	p, peers := socketPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, releaseOld, err := p.AcquireContext(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	oldPeer := <-peers
	defer oldPeer.Close()
	p.Invalidate("one")
	current, releaseNew, err := p.Acquire("one")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseNew()
	peer := <-peers
	defer peer.Close()
	cancel()
	releaseOld()
	p.mu.Lock()
	entry := p.entries["one"]
	p.mu.Unlock()
	if entry == nil || entry.conn != current {
		t.Fatal("old callback removed replacement")
	}
	received := make(chan struct{})
	go func() {
		defer close(received)
		_, err := xpc.DecodeMessage(peer)
		if err != nil {
			t.Errorf("replacement closed: %v", err)
		}
	}()
	if err := current.Send(map[string]interface{}{"new": "generation"}); err != nil {
		t.Fatal(err)
	}
	awaitPool(t, received)
}

func TestServicePool_ReleaseCancellationRacesDoNotRetireNextOwner(t *testing.T) {
	p, _ := socketPool(t)
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		_, release, err := p.AcquireContext(ctx, "one")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); release() }()
		go func() { defer wg.Done(); cancel() }()
		wg.Wait()
		_, next, err := p.Acquire("one")
		if err != nil {
			t.Fatal(err)
		}
		next()
	}
}
