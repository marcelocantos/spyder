// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T112 — host↔device TCP port forward on iOS via go-ios usbmux proxy
// (iproxy-class intent). Same PortForward surface as Android.

package device

import (
	"errors"
	"fmt"
	"net"
	"sync"

	goios_ios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/forward"
)

// iosActiveForward is one live usbmux forward owned by the adapter.
type iosActiveForward struct {
	cl *forward.ConnListener
	pf PortForward
}

// iosForwardStore holds per-local-port listeners for an IOSAdapter.
// Separate from the main adapter mutex so Accept loops never need a.mu.
type iosForwardStore struct {
	mu   sync.Mutex
	byLocal map[int]*iosActiveForward
}

func (s *iosForwardStore) put(f *iosActiveForward) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byLocal == nil {
		s.byLocal = map[int]*iosActiveForward{}
	}
	s.byLocal[f.pf.LocalPort] = f
}

func (s *iosForwardStore) remove(localPort int) (*iosActiveForward, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.byLocal[localPort]
	if ok {
		delete(s.byLocal, localPort)
	}
	return f, ok
}

func (s *iosForwardStore) list(serial string) []PortForward {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PortForward, 0, len(s.byLocal))
	for _, f := range s.byLocal {
		if serial != "" && f.pf.Serial != serial {
			continue
		}
		out = append(out, f.pf)
	}
	return out
}

// freeLocalPort picks an unused host TCP port (for local_port=0).
var freeLocalPort = func() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// iosForwardStart is the go-ios Forward hook (tests replace).
var iosForwardStart = func(dev goios_ios.DeviceEntry, hostPort, phonePort uint16) (*forward.ConnListener, error) {
	return forward.Forward(dev, hostPort, phonePort)
}

// ForwardTCP starts a usbmux TCP forward: host localPort → device devicePort.
// localPort 0 selects an ephemeral host port. The listener stays up until
// UnforwardTCP (or process exit).
func (a *IOSAdapter) ForwardTCP(id string, localPort, devicePort int) (PortForward, error) {
	if a == nil {
		return PortForward{}, errors.New("ios adapter is nil")
	}
	if id == "" {
		return PortForward{}, errors.New("device identifier is empty")
	}
	if devicePort <= 0 || devicePort > 65535 {
		return PortForward{}, errors.New("device_port must be in 1..65535")
	}
	if localPort < 0 || localPort > 65535 {
		return PortForward{}, errors.New("local_port must be in 0..65535 (0 = ephemeral)")
	}
	if localPort == 0 {
		p, err := freeLocalPort()
		if err != nil {
			return PortForward{}, fmt.Errorf("pick local port: %w", err)
		}
		localPort = p
	}

	dev, err := a.goios.Session(id)
	if err != nil {
		return PortForward{}, fmt.Errorf("ios session %s: %w", id, err)
	}
	cl, err := iosForwardStart(dev, uint16(localPort), uint16(devicePort))
	if err != nil {
		return PortForward{}, fmt.Errorf("ios forward tcp:%d→tcp:%d: %w", localPort, devicePort, err)
	}
	pf := PortForward{
		Serial:     id,
		LocalPort:  localPort,
		DevicePort: devicePort,
		Spec:       fmt.Sprintf("tcp:%d tcp:%d", localPort, devicePort),
	}
	// If something was already on this local port, close it first.
	if old, ok := a.fwdStore.remove(localPort); ok && old.cl != nil {
		_ = old.cl.Close()
	}
	a.fwdStore.put(&iosActiveForward{cl: cl, pf: pf})
	return pf, nil
}

// UnforwardTCP stops the host listener for localPort.
func (a *IOSAdapter) UnforwardTCP(id string, localPort int) error {
	if a == nil {
		return errors.New("ios adapter is nil")
	}
	if localPort <= 0 {
		return errors.New("local_port must be positive")
	}
	f, ok := a.fwdStore.remove(localPort)
	if !ok {
		return fmt.Errorf("no ios forward on local_port %d", localPort)
	}
	if id != "" && f.pf.Serial != "" && f.pf.Serial != id {
		// Put it back — wrong device.
		a.fwdStore.put(f)
		return fmt.Errorf("forward on local_port %d belongs to %s, not %s", localPort, f.pf.Serial, id)
	}
	if f.cl != nil {
		if err := f.cl.Close(); err != nil {
			return fmt.Errorf("close ios forward: %w", err)
		}
	}
	return nil
}

// ListForwards returns active usbmux forwards tracked by this adapter
// for the given device UDID.
func (a *IOSAdapter) ListForwards(id string) ([]PortForward, error) {
	if a == nil {
		return nil, errors.New("ios adapter is nil")
	}
	return a.fwdStore.list(id), nil
}
