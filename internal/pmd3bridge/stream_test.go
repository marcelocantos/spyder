// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// stallReader is a generic inter-packet-deadline detector over any
// io.ReadCloser. Its correctness does not depend on what produces the
// bytes — exercising it against io.Pipe (🎯T26.4) is a more honest test
// than wrapping it around a fake bridge configured to go silent on
// demand. The io.Pipe case is the primitive; real-bridge coverage lives
// in the integration tier.

func TestStallReader_PassThrough(t *testing.T) {
	r, w := io.Pipe()
	cancel := func() {}
	sr := newStallReader("/unit-test", r, cancel, time.Second)
	defer sr.Close()

	go func() {
		_, _ = w.Write([]byte("hello world"))
		_ = w.Close()
	}()

	out, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(out) != "hello world" {
		t.Errorf("got %q; want hello world", out)
	}
}

func TestStallReader_ProgressResetsWindow(t *testing.T) {
	// Write a chunk every 50ms for 3 chunks under a 200ms deadline;
	// no stall should fire.
	r, w := io.Pipe()
	cancel := func() {}
	sr := newStallReader("/unit-test", r, cancel, 200*time.Millisecond)
	defer sr.Close()

	go func() {
		for i := 0; i < 3; i++ {
			_, _ = w.Write([]byte("chunk"))
			time.Sleep(50 * time.Millisecond)
		}
		_ = w.Close()
	}()

	out, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("ReadAll: %v (got %q)", err, out)
	}
	if string(out) != "chunkchunkchunk" {
		t.Errorf("got %q", out)
	}
}

func TestStallReader_FiresAfterDeadline(t *testing.T) {
	// Write one chunk, then leave the pipe silent. The watchdog cancels,
	// the pipe closes, the next Read returns interPacketStallError.
	r, w := io.Pipe()
	cancelled := make(chan struct{})
	cancel := func() {
		// On stall the watchdog calls cancel(); we simulate the prod
		// cancel-side-effect (closing the network connection) by closing
		// the pipe writer.
		select {
		case <-cancelled:
		default:
			close(cancelled)
			_ = w.Close()
		}
	}
	sr := newStallReader("/unit-test", r, cancel, 100*time.Millisecond)
	defer sr.Close()

	go func() {
		_, _ = w.Write([]byte("one-chunk"))
		// Then do nothing — let the stall watchdog fire.
	}()

	buf := make([]byte, 1024)
	// First Read gets the chunk.
	n, err := sr.Read(buf)
	if err != nil || string(buf[:n]) != "one-chunk" {
		t.Fatalf("first Read: n=%d err=%v buf=%q", n, err, buf[:n])
	}
	// Second Read should stall and eventually fail with our typed error.
	_, err = sr.Read(buf)
	if err == nil {
		t.Fatal("second Read: err = nil; want stall")
	}
	var stall *interPacketStallError
	if !errors.As(err, &stall) {
		t.Errorf("err type = %T (%v); want *interPacketStallError", err, err)
	}
	if !strings.Contains(err.Error(), "inter-packet deadline") {
		t.Errorf("unexpected message: %v", err)
	}
}

// TestClient_DrainErr_ClassifiesCorrectly covers the precedence of
// errors the streaming clients encounter when draining a response body.
func TestClient_DrainErr_ClassifiesCorrectly(t *testing.T) {
	c := &Client{fatal: func(error) {}}

	if err := c.drainErr("/x", nil); err != nil {
		t.Errorf("drainErr(nil) = %v; want nil", err)
	}
	if err := c.drainErr("/x", io.EOF); err != nil {
		t.Errorf("drainErr(EOF) = %v; want nil (clean stream end)", err)
	}

	// Typed stall beats context.Canceled precedence.
	stall := &interPacketStallError{endpoint: "/x", deadline: time.Second}
	var captured error
	c.fatal = func(err error) { captured = err }
	_ = c.drainErr("/x", stall)
	if captured == nil {
		t.Error("stall error did not fire fatal")
	}
}
