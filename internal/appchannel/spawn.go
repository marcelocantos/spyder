// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"slices"
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
// advertise MethodSpawnInstance. The instance is identified as the first
// session that appears after the spawn call that wasn't present before it —
// spawns per factory are assumed sequential (the caller serialises them).
//
// This is the server-medium spawn backend of the T92 launcher: a game server
// is a device factory, an instance is an abstract device, and each instance
// gets the full app-channel monitor surface for free.
func (m *Manager) SpawnInstance(ctx context.Context, factory *Session, req SpawnRequest, timeout time.Duration) (*Session, error) {
	if factory == nil {
		return nil, fmt.Errorf("appchannel: spawn: nil factory session")
	}
	if hi := factory.HelloInfo(); hi != nil && !slices.Contains(hi.Methods, MethodSpawnInstance) {
		return nil, fmt.Errorf("appchannel: session %s is not a factory (no %s capability)",
			factory.ID, MethodSpawnInstance)
	}

	before := map[string]bool{}
	for _, s := range m.Sessions() {
		before[s.ID] = true
	}

	if _, err := factory.Call(ctx, MethodSpawnInstance, req, timeout); err != nil {
		return nil, fmt.Errorf("appchannel: spawn_instance call: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, s := range m.Sessions() {
			if !before[s.ID] {
				return s, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("appchannel: spawned instance did not connect within %s", timeout)
}
