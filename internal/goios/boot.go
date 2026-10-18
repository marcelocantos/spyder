// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	ioshttp "github.com/danielpaulus/go-ios/ios/http"
	"github.com/danielpaulus/go-ios/ios/tunnel"
)

// BootSessionUUID is the device's RSD BootSessionUUID. It changes when
// the device boots and is stable until then. An empty id with a nil
// error does not happen; failure returns an error and the caller must
// treat a missing id as "not observed", not as a reboot.
func (r *Resolver) BootSessionUUID(ctx context.Context, udid string) (string, error) {
	if r == nil || udid == "" {
		return "", errors.New("goios: boot session requires a resolver and udid")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	if snap, ok := r.boots[udid]; ok && snap.id != "" && time.Since(snap.at) < cacheTTL {
		id := snap.id
		r.mu.Unlock()
		return id, nil
	}
	r.mu.Unlock()

	type result struct {
		id  string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		id, err := r.readBootSessionUUID(udid)
		ch <- result{id: id, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case got := <-ch:
		if got.err != nil || got.id == "" {
			if got.err == nil {
				got.err = errors.New("goios: rsd handshake has no BootSessionUUID")
			}
			return "", got.err
		}
		r.mu.Lock()
		if r.boots == nil {
			r.boots = map[string]bootSnap{}
		}
		r.boots[udid] = bootSnap{id: got.id, at: time.Now()}
		r.mu.Unlock()
		return got.id, nil
	}
}

// ForgetBootSession drops a cached boot id so the next probe reads the
// device again. The usbmux detach edge calls this. It does not drop the
// tunnel.
func (r *Resolver) ForgetBootSession(udid string) {
	if r == nil || udid == "" {
		return
	}
	r.mu.Lock()
	delete(r.boots, udid)
	r.mu.Unlock()
}

func (r *Resolver) readBootSessionUUID(udid string) (string, error) {
	info, err := tunnel.TunnelInfoForDevice(udid, r.tunnelHost, r.tunnelPort)
	if err != nil {
		return "", fmt.Errorf("goios: tunnel info for boot session %s: %w", udid, err)
	}
	dev, err := ios.GetDevice(udid)
	if err != nil {
		return "", fmt.Errorf("goios: device for boot session %s: %w", udid, err)
	}
	dev.UserspaceTUN = info.UserspaceTUN
	dev.UserspaceTUNHost = r.tunnelHost
	dev.UserspaceTUNPort = info.UserspaceTUNPort
	conn, err := ios.ConnectTUNDevice(info.Address, info.RsdPort, dev)
	if err != nil {
		return "", fmt.Errorf("goios: rsd connect for boot session %s: %w", udid, err)
	}
	h, err := ioshttp.NewHttpConnection(conn)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("goios: rsd http for boot session %s: %w", udid, err)
	}
	defer h.Close()
	xpcConn, err := ios.CreateXpcConnection(h)
	if err != nil {
		return "", fmt.Errorf("goios: rsd xpc for boot session %s: %w", udid, err)
	}
	msg, err := xpcConn.ReceiveOnClientServerStream()
	if err != nil {
		return "", fmt.Errorf("goios: rsd handshake for boot session %s: %w", udid, err)
	}
	id := bootSessionUUID(msg)
	if id == "" {
		return "", fmt.Errorf("goios: rsd handshake for %s has no BootSessionUUID", udid)
	}
	return id, nil
}

// bootSessionUUID reads Properties.BootSessionUUID. go-ios decodes the
// live handshake value as uuid.UUID, which is not a Go string. The
// fixture handshake stores the same field as a string.
func bootSessionUUID(msg map[string]interface{}) string {
	if msg == nil {
		return ""
	}
	props, _ := msg["Properties"].(map[string]interface{})
	if props == nil {
		return ""
	}
	switch v := props["BootSessionUUID"].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}
