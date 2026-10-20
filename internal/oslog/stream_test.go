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
	stream := &Stream{conn: connection, frames: make(chan traceBatch, 2), closeCh: make(chan struct{}), Records: make(chan Record, capacity)}
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
	stream.frames <- traceBatch{Payload: word16(cmdStruct, 1)}
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
	stream.frames <- traceBatch{Payload: heartbeatPrefix}
	stream.frames <- traceBatch{Payload: defineTableFrame("Logs", []string{"message"})}
	stream.frames <- traceBatch{Payload: buildFrame(word16(cmdStruct, 0), word16(cmdEndRow, 0))}
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
	stream.frames <- traceBatch{Payload: defineTableFrame("Logs", []string{"message"})}
	stream.frames <- traceBatch{Payload: buildFrame(word16(cmdStruct, 0), word16(cmdEndRow, 0))}
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
	failed.frames <- traceBatch{Payload: word16(cmdStruct, 1)}
	receiveClosed(t, failed)
	healthy.frames <- traceBatch{Payload: defineTableFrame("Logs", []string{"message"})}
	healthy.frames <- traceBatch{Payload: buildFrame(word16(cmdStruct, 0), word16(cmdEndRow, 0))}
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

func TestStreamPendingInstructionFailsEveryTerminalPath(t *testing.T) {
	for _, mode := range []string{"close", "cancel", "disconnect", "frames_closed"} {
		for _, partial := range [][]byte{{0x00}, {0x00, 0x80}} {
			connection := &controlledConnection{closed: make(chan struct{}), cause: errors.New("injected DTX EOF")}
			stream := &Stream{conn: connection, frames: make(chan traceBatch), closeCh: make(chan struct{}), Records: make(chan Record)}
			ctx, cancel := context.WithCancel(context.Background())
			go stream.pump(ctx)
			stream.frames <- traceBatch{Payload: partial}
			// Receipt on an unbuffered channel ensures the partial instruction
			// is decoded before the next select can observe termination.
			switch mode {
			case "close":
				stream.Close()
			case "cancel":
				cancel()
			case "disconnect":
				connection.Close()
			case "frames_closed":
				close(stream.frames)
			}
			receiveClosed(t, stream)
			cancel()
			stream.Close()
			if !errors.Is(stream.Err(), ErrCaptureIntegrity) || stream.LastDecodeError() == nil || !strings.Contains(stream.Err().Error(), "truncated") {
				t.Fatalf("%s lost pending terminal instruction: %v", mode, stream.Err())
			}
			if mode == "disconnect" && !strings.Contains(stream.Err().Error(), "injected DTX EOF") {
				t.Fatal("terminal decoder check erased transport cause")
			}
			if mode == "frames_closed" && !strings.Contains(stream.Err().Error(), "frame channel closed") {
				t.Fatal("terminal decoder check erased channel cause")
			}
			select {
			case <-connection.Closed():
			default:
				t.Fatal("pending failure leaked owned connection")
			}
		}
	}
}

func TestStreamHeartbeatBetweenImmediatePartsPreservesRows(t *testing.T) {
	connection := &controlledConnection{closed: make(chan struct{}), cause: errors.New("injected DTX EOF")}
	stream := &Stream{conn: connection, frames: make(chan traceBatch), closeCh: make(chan struct{}), Records: make(chan Record, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	go stream.pump(ctx)
	corpus := incrementalCorpus()
	for _, payload := range [][]byte{corpus[:1], heartbeatPrefix, nil, corpus[1:]} {
		stream.frames <- traceBatch{Payload: payload}
	}
	for _, message := range []string{"got it", "second"} {
		select {
		case row, ok := <-stream.Records:
			if !ok || row.PID != 42 || row.ImageName != "MyApp" || row.Message != message {
				t.Fatalf("lost or duplicated row: %+v err=%v", row, stream.Err())
			}
		case <-time.After(time.Second):
			t.Fatal("split immediate did not resume")
		}
	}
	cancel()
	receiveClosed(t, stream)
	if stream.Err() != nil {
		t.Fatalf("complete incremental stream failed: %v", stream.Err())
	}
}
