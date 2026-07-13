// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// TestSpawnInstance_FactoryDouble is the 🎯T92.1 oracle: a test-double game
// server (a fakeApp advertising spawn_instance) acts as a device factory —
// when spyder calls SpawnInstance, the factory manufactures an instance that
// dials spyder back as its own app-channel session. Proves the spawn protocol
// and machinery with no ge dependency; the real ge server conforms to the same
// spawn_instance method later.
func TestSpawnInstance_FactoryDouble(t *testing.T) {
	m, l := startManagerAndListener(t)
	addr := fmt.Sprintf("127.0.0.1:%d", l.Port)

	factory := newFakeApp(t, addr, []string{MethodSpawnInstance})
	t.Cleanup(factory.close)

	var mu sync.Mutex
	var instances []net.Conn
	factory.on(MethodSpawnInstance, func(params []byte) (any, error) {
		var req SpawnRequest
		if err := UnpackParams(params, &req); err != nil {
			return nil, err
		}
		// The factory forks an instance that dials spyder back as its own
		// session, named after the requested game.
		conn := dialInstance(t, req.AppChannel, req.Game)
		mu.Lock()
		instances = append(instances, conn)
		mu.Unlock()
		return map[string]any{"instance_id": req.InstanceID, "ok": true}, nil
	})
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range instances {
			_ = c.Close()
		}
	})

	factorySession := waitForSession(t, l)

	inst, err := m.SpawnInstance(context.Background(), factorySession,
		SpawnRequest{Game: "tiltbuggy", AppChannel: addr, InstanceID: "i1"}, 5*time.Second)
	if err != nil {
		t.Fatalf("SpawnInstance: %v", err)
	}
	if inst.ID == factorySession.ID {
		t.Fatal("SpawnInstance returned the factory session, not a new instance")
	}
	if hi := inst.HelloInfo(); hi == nil || hi.AppName != "tiltbuggy" {
		t.Fatalf("instance hello unexpected: %+v", hi)
	}

	// Each instance is its own session (the per-instance rule, ex-T91.6): the
	// factory + one instance = two independently addressable sessions.
	if got := len(m.Sessions()); got != 2 {
		t.Fatalf("want 2 sessions (factory + instance), got %d", got)
	}
}

// TestSpawnInstance_NotAFactory rejects a session that doesn't advertise the
// spawn capability.
func TestSpawnInstance_NotAFactory(t *testing.T) {
	m, l := startManagerAndListener(t)
	addr := fmt.Sprintf("127.0.0.1:%d", l.Port)
	app := newFakeApp(t, addr, []string{MethodPing}) // no spawn_instance
	t.Cleanup(app.close)
	s := waitForSession(t, l)

	if _, err := m.SpawnInstance(context.Background(), s,
		SpawnRequest{Game: "x", AppChannel: addr}, time.Second); err == nil {
		t.Fatal("SpawnInstance should reject a non-factory session")
	}
}

// dialInstance connects a minimal app-channel client (an "instance") that
// sends a hello under name and drains frames to stay alive.
func dialInstance(t *testing.T, addr, name string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Errorf("instance dial: %v", err)
		return nil
	}
	hp, _ := PackParams(Hello{AppName: name, AppVersion: "test", Methods: []string{MethodPing}})
	if err := WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: hp}); err != nil {
		t.Errorf("instance hello: %v", err)
		return conn
	}
	go func() {
		for {
			if _, err := ReadFrame(conn); err != nil {
				return
			}
		}
	}()
	return conn
}
