// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	goios_ios "github.com/danielpaulus/go-ios/ios"
	dtx "github.com/danielpaulus/go-ios/ios/dtx_codec"
)

// Channel identifiers used to open the activitytracetap stream over
// DTX. RSD-capable devices (iOS 17+) take the dtservicehub path; the
// older two are kept for legacy compatibility but the practical
// target is iOS 17+ since that's where the lockdown-level log
// services started filtering third-party app output.
const (
	channelActivityTraceTap = "com.apple.instruments.server.services.activitytracetap"
	serviceRsd              = "com.apple.instruments.dtservicehub"
	serviceUsbmuxd          = "com.apple.instruments.remoteserver"
	serviceUsbmuxdiOS14     = "com.apple.instruments.remoteserver.DVTSecureSocketProxy"
)

// heartbeatPrefix marks frames that aren't opcode streams but instead
// out-of-band keep-alives carrying an empty bplist. Filter them
// before invoking the decoder.
var heartbeatPrefix = []byte("bplist")

// ErrCaptureIntegrity marks loss after a DTX capture opened. Falling back to
// another channel cannot recover the missing interval or undecodable batch.
var ErrCaptureIntegrity = errors.New("oslog capture integrity failed")

type streamConnection interface {
	Close() error
	Closed() <-chan struct{}
	Err() error
}

// Stream is a live OSLog stream from one device, opened via the DTX
// activitytracetap channel. Records are delivered on the Records
// channel until the stream is closed (context cancellation, device
// disconnect, or explicit Close).
type Stream struct {
	conn         streamConnection
	channel      *dtx.Channel
	frames       chan traceBatch
	closeOnce    sync.Once
	closeCh      chan struct{}
	Records      chan Record
	errMu        sync.RWMutex
	err          error
	decodeErr    error
	batchCapture *batchCapture
}

// frameDispatcher pushes incoming DTX message payloads onto the
// stream's frame channel. The activitytracetap channel delivers each
// log batch as a single binary blob in Payload[0], with the
// expects-reply flag set false (one-way push).
type frameDispatcher struct {
	frames  chan traceBatch
	closeCh chan struct{}
}

func (d frameDispatcher) Dispatch(msg dtx.Message) {
	if len(msg.Payload) == 0 {
		return
	}
	blob, ok := msg.Payload[0].([]byte)
	if !ok {
		return
	}
	select {
	case d.frames <- traceBatch{Payload: blob, MessageType: uint32(msg.PayloadHeader.MessageType),
		ChannelCode: msg.ChannelCode, Identifier: msg.Identifier, FragmentIndex: msg.FragmentIndex, Fragments: msg.Fragments}:
	case <-d.closeCh:
	}
}

// Open establishes the DTX connection, requests the activitytracetap
// channel, sends setConfig: + start, and begins draining inbound
// frames into a decoder goroutine. Records appear on the returned
// Stream's Records channel.
//
// Returns a clear error if the channel can't be opened — typical
// causes: developer disk image not mounted on the device, RSD tunnel
// not established, iOS version that doesn't support this channel.
// Callers can downgrade to the ostrace_relay path on error.
func Open(ctx context.Context, dev goios_ios.DeviceEntry) (_ *Stream, retErr error) {
	capture, err := batchCaptureFromEnvironment(dev.Properties.SerialNumber)
	if err != nil {
		return nil, fmt.Errorf("%w: diagnostic batch configuration: %w", ErrCaptureIntegrity, err)
	}
	handedOff := false
	defer func() {
		if capture != nil && !handedOff {
			retErr = errors.Join(retErr, capture.finish(retErr))
		}
	}()
	conn, err := connectInstruments(dev)
	if err != nil {
		return nil, fmt.Errorf("oslog: DTX connect: %w", err)
	}

	frames := make(chan traceBatch, 32)
	closeCh := make(chan struct{})
	disp := frameDispatcher{frames: frames, closeCh: closeCh}

	// activitytracetap pushes its data on the global / default
	// channel (code -1 / uint32-max). The connection-level
	// MessageDispatcher is the right hook: it catches messages that
	// don't belong to a channel-code-specific Channel registration,
	// which is exactly the bucket the binary log batches arrive in.
	conn.MessageDispatcher = disp
	channel := conn.RequestChannelIdentifier(channelActivityTraceTap, disp)

	if _, err := channel.MethodCall("setConfig:", defaultConfig()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("oslog: setConfig: %w", err)
	}
	if err := channel.MethodCallAsync("start"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("oslog: start: %w", err)
	}

	s := &Stream{
		conn:         conn,
		channel:      channel,
		frames:       frames,
		closeCh:      closeCh,
		Records:      make(chan Record, 64),
		batchCapture: capture,
	}

	handedOff = true
	go s.pump(ctx)

	return s, nil
}

// connectInstruments opens a DTX-based instruments connection,
// preferring the RSD-shimmed `dtservicehub` for iOS-17+ devices and
// falling back to the legacy `remoteserver` paths otherwise. Mirrors
// the equivalent logic in go-ios's internal instruments package.
func connectInstruments(dev goios_ios.DeviceEntry) (*dtx.Connection, error) {
	if dev.SupportsRsd() {
		return dtx.NewTunnelConnection(dev, serviceRsd)
	}
	c, err := dtx.NewUsbmuxdConnection(dev, serviceUsbmuxd)
	if err == nil {
		return c, nil
	}
	return dtx.NewUsbmuxdConnection(dev, serviceUsbmuxdiOS14)
}

// defaultConfig returns the standard setConfig: dictionary used to
// initialise activitytracetap. The settings match Xcode Console.app's
// defaults: all severities preserved, all PIDs, signposts-and-logs
// mode, PID-to-exec-name mapping enabled. ur=500 is the update rate
// (in milliseconds between batch flushes).
func defaultConfig() map[string]any {
	return map[string]any{
		"bm":                        0,
		"combineDataScope":          0,
		"machTimebaseDenom":         3,
		"machTimebaseNumer":         125,
		"onlySignposts":             0,
		"pidToInjectCombineDYLIB":   "-1",
		"predicate":                 "(messageType == info OR messageType == debug OR messageType == default OR messageType == error OR messageType == fault)",
		"signpostsAndLogs":          1,
		"trackPidToExecNameMapping": true,
		"enableHTTPArchiveLogging":  false,
		"targetPID":                 -3,
		"trackExpiredPIDs":          1,
		"ur":                        500,
	}
}

// pump drains the inbound frame channel, runs the decoder, and emits
// Records. It terminates when ctx fires or the connection drops.
func (s *Stream) pump(ctx context.Context) {
	defer close(s.Records)
	defer s.finishBatchCapture()
	defer s.Close()
	var disconnected <-chan struct{}
	if s.conn != nil {
		disconnected = s.conn.Closed()
	}
	dec := NewDecoder()
	var buf []Record
	for {
		select {
		case <-ctx.Done():
			s.Close()
			return
		case <-s.closeCh:
			return
		case <-disconnected:
			// Explicit Close and context cancellation also close the transport.
			// When both selects are ready, neither is a capture failure.
			s.noteDisconnect(ctx)
			return
		case batch, ok := <-s.frames:
			if !ok {
				s.errMu.Lock()
				s.err = fmt.Errorf("%w: DTX frame channel closed", ErrCaptureIntegrity)
				s.errMu.Unlock()
				return
			}
			if s.batchCapture != nil {
				if err := s.batchCapture.writeBatch(batch); err != nil {
					s.errMu.Lock()
					s.err = fmt.Errorf("%w: diagnostic batch retention: %w", ErrCaptureIntegrity, err)
					s.errMu.Unlock()
					return
				}
			}
			frame := batch.Payload
			if bytes.HasPrefix(frame, heartbeatPrefix) || len(frame) == 0 {
				continue
			}
			buf = buf[:0]
			out, err := dec.Decode(frame, buf)
			if err != nil {
				s.errMu.Lock()
				s.decodeErr = err
				s.err = fmt.Errorf("%w: activitytracetap batch decode: %w", ErrCaptureIntegrity, err)
				s.errMu.Unlock()
				return
			}
			for _, r := range out {
				select {
				case s.Records <- r:
				case <-ctx.Done():
					s.Close()
					return
				case <-s.closeCh:
					return
				case <-disconnected:
					s.noteDisconnect(ctx)
					return
				}
			}
		}
	}
}

func (s *Stream) noteDisconnect(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	select {
	case <-s.closeCh:
		return
	default:
	}
	s.errMu.Lock()
	s.err = fmt.Errorf("%w: DTX connection closed: %v", ErrCaptureIntegrity, s.conn.Err())
	s.errMu.Unlock()
}

// Close terminates the stream and tears down the DTX connection.
// Safe to call multiple times.
func (s *Stream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeCh)
		if s.conn != nil {
			s.conn.Close()
		}
	})
	return nil
}

// Err returns the sticky terminal capture cause. Explicit Close and context
// cancellation leave it nil; an interrupted/undecodable stream is not clean.
func (s *Stream) Err() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	return s.err
}

// LastDecodeError retains the decoder cause for existing callers. A decoding
// failure now ends the stream because the omitted records cannot be counted.
func (s *Stream) LastDecodeError() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	if s.decodeErr == nil {
		return nil
	}
	return errors.New(s.decodeErr.Error())
}
