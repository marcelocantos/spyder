// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"time"
)

// SpawnRequest is the payload spyder sends to a factory's spawn_instance
// method (🎯T92.1). The factory forks a game instance and points it at
// AppChannel so the instance dials spyder back as its own app-channel session.
type SpawnRequest struct {
	Game       string `msgpack:"game" json:"game"`
	AppChannel string `msgpack:"app_channel" json:"app_channel"` // host:port the instance dials
	InstanceID string `msgpack:"instance_id,omitempty" json:"instance_id,omitempty"`
}

// SpawnInstance asks a factory session to spawn a game instance and returns
// the instance's new session once it connects (🎯T92.1). factory must
// advertise MethodSpawnInstance. Each spawn gets its own loopback callback
// listener so concurrent spawns and unrelated app sessions stay distinct.
//
// This is the server-medium spawn backend of the T92 launcher: a game server
// is a device factory, an instance is an abstract device, and each instance
// gets the full app-channel monitor surface for free.
func (m *Manager) SpawnInstance(ctx context.Context, factory *Session, req SpawnRequest, timeout time.Duration) (*Session, error) {
	if factory == nil {
		return nil, fmt.Errorf("appchannel: spawn: nil factory session")
	}
	if hi := factory.HelloInfo(); hi != nil && !slices.Contains(MethodNames(hi.Methods), MethodSpawnInstance) {
		return nil, fmt.Errorf("appchannel: session %s is not a factory (no %s capability)",
			factory.ID, MethodSpawnInstance)
	}

	listener, err := m.Start(StartParams{Owner: factory.Owner})
	if err != nil {
		return nil, err
	}
	adopted := false
	defer func() {
		if !adopted {
			listener.Stop()
		}
	}()
	req.AppChannel = net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Port))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if _, err := factory.Call(ctx, MethodSpawnInstance, req, timeout); err != nil {
		return nil, fmt.Errorf("appchannel: spawn_instance call: %w", err)
	}

	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("appchannel: spawned instance did not connect: %w", err)
		}
		for _, s := range listener.Sessions() {
			if s.HelloInfo() != nil {
				adopted = true
				// Close the accepting socket now, while the one session
				// keeps its connected socket until disconnect.
				_ = listener.listener.Close()
				go func() {
					<-s.done
					listener.Stop()
				}()
				return s, nil
			}
		}
		select {
		case <-ctx.Done():
		case <-poll.C:
		}
	}
}
