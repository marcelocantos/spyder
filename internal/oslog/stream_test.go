// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type controlledConnection struct {
	closed chan struct{}
	once   sync.Once
	cause  error
}

func (c *controlledConnection) Close() error            { c.once.Do(func() { close(c.closed) }); return nil }
func (c *controlledConnection) Closed() <-chan struct{} { return c.closed }
func (c *controlledConnection) Err() error              { return c.cause }

func controlledStream(t *testing.T, buffered bool) (*Stream, *controlledConnection, context.CancelFunc) {
	t.Helper()
	connection := &controlledConnection{closed: make(chan struct{}), cause: errors.New("injected DTX EOF")}
	capacity := 0
	if buffered {
		capacity = 1
	}
	stream := &Stream{conn: connection, frames: make(chan []byte, 2), closeCh: make(chan struct{}), Records: make(chan Record, capacity)}
	ctx, cancel := context.WithCancel(context.Background())
	go stream.pump(ctx)
	t.Cleanup(func() {
		cancel()
		stream.Close()
		for range stream.Records {
		}
	})
	return stream, connection, cancel
}

func receiveClosed(t *testing.T, stream *Stream) {
	t.Helper()
	select {
	case _, ok := <-stream.Records:
		if ok {
			t.Fatal("unexpected emitted row")
		}
	case <-time.After(time.Second):
		t.Fatal("capture reader stayed alive after a terminal DTX failure")
	}
}

func TestStreamDisconnectEndsReaderWithIntegrityCause(t *testing.T) {
	stream, connection, _ := controlledStream(t, true)
	connection.Close()
	receiveClosed(t, stream)
	if !errors.Is(stream.Err(), ErrCaptureIntegrity) || !strings.Contains(stream.Err().Error(), "injected DTX EOF") {
		t.Fatalf("disconnect lost its integrity cause: %v", stream.Err())
	}
}

func TestStreamDecodeLossEndsReaderWithIntegrityCause(t *testing.T) {
	stream, connection, _ := controlledStream(t, true)
	// A struct opcode with an empty decoder stack is a real decoder failure.
	stream.frames <- word16(cmdStruct, 1)
	receiveClosed(t, stream)
	if !errors.Is(stream.Err(), ErrCaptureIntegrity) || !strings.Contains(stream.Err().Error(), "underflow") || stream.LastDecodeError() == nil {
		t.Fatalf("undecodable batch was silently dropped: %v", stream.Err())
	}
	select {
	case <-connection.Closed():
	default:
		t.Fatal("decoder failure leaked its owned connection")
	}
	// Explicit cleanup must not erase the earlier loss.
	stream.Close()
	if !errors.Is(stream.Err(), ErrCaptureIntegrity) {
		t.Fatal("cleanup erased capture failure")
	}
}

func TestStreamOwnedCloseAndCancellationRemainClean(t *testing.T) {
	for index := 0; index < 100; index++ {
		for _, mode := range []string{"close", "cancel"} {
			stream, _, cancel := controlledStream(t, true)
			if mode == "close" {
				stream.Close()
			} else {
				cancel()
				stream.Close()
			}
			receiveClosed(t, stream)
			if stream.Err() != nil {
				t.Fatalf("normal %s poisoned integrity: %v", mode, stream.Err())
			}
		}
	}
}

func TestStreamValidHeartbeatDoesNotLookLikeDecoderLoss(t *testing.T) {
	stream, _, cancel := controlledStream(t, true)
	stream.frames <- heartbeatPrefix
	stream.frames <- defineTableFrame("Logs", []string{"message"})
	stream.frames <- buildFrame(word16(cmdStruct, 0), word16(cmdEndRow, 0))
	select {
	case _, ok := <-stream.Records:
		if !ok {
			t.Fatalf("valid stream closed: %v", stream.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("valid decoder row never arrived")
	}
	cancel()
	receiveClosed(t, stream)
	if stream.Err() != nil {
		t.Fatalf("valid stream poisoned integrity: %v", stream.Err())
	}
}

func TestStreamDisconnectUnblocksFullRecordDelivery(t *testing.T) {
	stream, connection, _ := controlledStream(t, false)
	stream.frames <- defineTableFrame("Logs", []string{"message"})
	stream.frames <- buildFrame(word16(cmdStruct, 0), word16(cmdEndRow, 0))
	connection.Close()
	// A queued row may win once before disconnect; continuously drain so both
	// outer and blocked-send shutdown paths are allowed to finish.
	done := make(chan struct{})
	go func() {
		for range stream.Records {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnect left record delivery blocked")
	}
	if !errors.Is(stream.Err(), ErrCaptureIntegrity) {
		t.Fatalf("blocked delivery lost disconnect: %v", stream.Err())
	}
}

func TestStreamFailureDoesNotCloseAnotherCapture(t *testing.T) {
	failed, _, _ := controlledStream(t, true)
	healthy, connection, cancel := controlledStream(t, true)
	failed.frames <- word16(cmdStruct, 1)
	receiveClosed(t, failed)
	healthy.frames <- defineTableFrame("Logs", []string{"message"})
	healthy.frames <- buildFrame(word16(cmdStruct, 0), word16(cmdEndRow, 0))
	select {
	case _, ok := <-healthy.Records:
		if !ok {
			t.Fatal("another capture was closed by the failed stream")
		}
	case <-time.After(time.Second):
		t.Fatal("another capture stopped delivering")
	}
	select {
	case <-connection.Closed():
		t.Fatal("failure closed another capture's transport")
	default:
	}
	cancel()
	receiveClosed(t, healthy)
	if healthy.Err() != nil {
		t.Fatalf("another capture inherited failure: %v", healthy.Err())
	}
}
