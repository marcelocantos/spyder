// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"fmt"
	"testing"
	"time"
)

func TestSessionLifecycleObservers(t *testing.T) {
	m, l := startManagerAndListener(t)
	type observed struct{ kind, id string }
	events := make(chan observed, 2)
	m.SetOnSession(func(kind string, s *Session) {
		// Observer may re-enter the manager; it must run outside its lock.
		_, present := m.GetSession(s.ID)
		if (kind == "connected") != present {
			t.Errorf("wrong lifecycle boundary: %s present=%v", kind, present)
		}
		events <- observed{kind, s.ID}
	})
	app := newFakeApp(t, fmt.Sprintf("127.0.0.1:%d", l.Port), nil)
	var connected observed
	select {
	case connected = <-events:
		if connected.kind != "connected" {
			t.Fatal(connected)
		}
	case <-time.After(time.Second):
		t.Fatal("missing connected event")
	}
	app.close()
	select {
	case e := <-events:
		if e.kind != "disconnected" || e.id != connected.id {
			t.Fatalf("wrong disconnect: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing disconnected event")
	}
}
