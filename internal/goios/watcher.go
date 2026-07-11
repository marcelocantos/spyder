// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/danielpaulus/go-ios/ios"
)

// UsbmuxKind is attach or detach.
type UsbmuxKind int

const (
	UsbmuxAttach UsbmuxKind = iota + 1
	UsbmuxDetach
)

// UsbmuxEvent is one device lifecycle signal from usbmuxd.
type UsbmuxEvent struct {
	Kind UsbmuxKind
	UDID string
}

// UsbmuxSource yields attach/detach events. Production uses ios.Listen();
// tests inject a fake (🎯T89.2 oracle).
type UsbmuxSource interface {
	// Next blocks until the next event or ctx cancel / permanent error.
	Next(ctx context.Context) (UsbmuxEvent, error)
	Close() error
}

// TunnelRecovery is the subset of Resolver used by the watcher so tests
// can inject a recorder without a full Resolver.
type TunnelRecovery interface {
	DropTunnel(udid string)
	ReestablishTunnel(udid string) error
	Invalidate(udid string)
}

// ServicePoolInvalidator drops cached service connections for a UDID.
// IOSAdapter implements this for its pools (🎯T89.2).
type ServicePoolInvalidator interface {
	InvalidateDevice(udid string)
}

// WatchUsbmux runs until ctx is done. On detach: DropTunnel + Invalidate pools.
// On attach: Invalidate + ReestablishTunnel (proactive rebuild).
func WatchUsbmux(ctx context.Context, src UsbmuxSource, rec TunnelRecovery, pools ServicePoolInvalidator) {
	if src == nil || rec == nil {
		return
	}
	defer func() { _ = src.Close() }()
	for {
		ev, err := src.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("goios usbmux watch: next failed; stopping", "error", err.Error())
			return
		}
		if ev.UDID == "" {
			continue
		}
		switch ev.Kind {
		case UsbmuxDetach:
			slog.Info("goios usbmux: detach", "udid", ev.UDID)
			rec.DropTunnel(ev.UDID)
			if pools != nil {
				pools.InvalidateDevice(ev.UDID)
			}
		case UsbmuxAttach:
			slog.Info("goios usbmux: attach", "udid", ev.UDID)
			rec.Invalidate(ev.UDID)
			if pools != nil {
				pools.InvalidateDevice(ev.UDID)
			}
			if err := rec.ReestablishTunnel(ev.UDID); err != nil {
				// Attach may race the device becoming fully enumerable; lazy
				// T89.1 path on next consumer call is the safety net.
				slog.Info("goios usbmux: attach re-establish deferred",
					"udid", ev.UDID, "error", err.Error())
			}
		}
	}
}

// RealUsbmuxSource wraps go-ios ios.Listen().
type RealUsbmuxSource struct {
	next  func() (ios.AttachedMessage, error)
	close func() error
}

// NewRealUsbmuxSource opens a usbmux listen connection. Returns an error if
// usbmuxd is unreachable (watcher simply not started).
func NewRealUsbmuxSource() (*RealUsbmuxSource, error) {
	next, closeFn, err := ios.Listen()
	if err != nil {
		return nil, fmt.Errorf("goios: usbmux listen: %w", err)
	}
	return &RealUsbmuxSource{next: next, close: closeFn}, nil
}

// Next implements UsbmuxSource. It ignores the context during the blocking
// read (usbmux has no cancel); callers should Close() on shutdown.
func (s *RealUsbmuxSource) Next(ctx context.Context) (UsbmuxEvent, error) {
	// Prefer ctx done over blocking forever when possible: race the listen
	// call in a goroutine. If ctx cancels mid-read, Close unblocks.
	type result struct {
		msg ios.AttachedMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := s.next()
		ch <- result{msg, err}
	}()
	select {
	case <-ctx.Done():
		return UsbmuxEvent{}, ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return UsbmuxEvent{}, res.err
		}
		udid := res.msg.Properties.SerialNumber
		switch {
		case res.msg.DeviceAttached():
			return UsbmuxEvent{Kind: UsbmuxAttach, UDID: udid}, nil
		case res.msg.DeviceDetached():
			return UsbmuxEvent{Kind: UsbmuxDetach, UDID: udid}, nil
		default:
			// Unknown message — skip by returning empty UDID for the loop.
			return UsbmuxEvent{}, nil
		}
	}
}

// Close implements UsbmuxSource.
func (s *RealUsbmuxSource) Close() error {
	if s.close != nil {
		return s.close()
	}
	return nil
}
