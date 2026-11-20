// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// ensure_session pings an existing session and closes it when the ping
// fails. That close came before the reservation check, so a caller who
// does not hold the device could kill the holder's app channel while the
// app was busy loading (DH-30).
func TestEnsureSessionByNonHolderKeepsHoldersSession(t *testing.T) {
	h := newHandlerWithStubs(t, &stubAdapter{}, &stubAdapter{})
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	resv, err := reservations.New("")
	if err != nil {
		t.Fatal(err)
	}
	h.reservations = resv
	if _, err := resv.Acquire("iPad", "job-A", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	key := appchannel.AppKey{DeviceID: "00008103-001122334455667A", BundleID: "com.example.app"}
	l, err := h.appChannel.GetOrCreateListener(key)
	if err != nil {
		t.Fatal(err)
	}
	// job-A's app completes hello, then is busy and does not answer pings.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello, err := appchannel.PackParams(appchannel.Hello{AppName: "game", AppVersion: "1",
		Methods: appchannel.MethodDescriptors(appchannel.MethodPing)})
	if err != nil {
		t.Fatal(err)
	}
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: hello}); err != nil {
		t.Fatal(err)
	}
	if _, err := appchannel.ReadFrame(conn); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(l.Sessions()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(l.Sessions()) == 0 {
		t.Fatal("no session")
	}

	res, _ := h.Dispatch(context.Background(), "ensure_session", map[string]any{
		"device": "iPad", "bundle_id": "com.example.app", "owner": "job-B",
	})
	if res == nil || !res.IsError {
		t.Fatalf("job-B should be refused: %+v", res)
	}
	time.Sleep(100 * time.Millisecond)
	if len(l.Sessions()) == 0 {
		t.Errorf("job-B was refused (%s), but job-A's app-channel session was closed anyway", resultErrorText(t, res))
	}
}
