// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/notify"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/verify"
)

func TestNotificationsResolveOfflineInventoryAndAppExec(t *testing.T) {
	root := t.TempDir()
	t.Setenv(paths.DataDirEnv, root)
	if err := os.WriteFile(filepath.Join(root, "inventory.json"), []byte(`[{"alias":"iPad","platform":"ios","ios_uuid":"00008103-001122334455667A","ios_coredevice":"12345678-1234-1234-1234-123456789abc"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := notify.Open(filepath.Join(root, "notifications.json"), func(ctx context.Context, _, _ string) error { <-ctx.Done(); return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(&stubAdapter{}, &stubAdapter{})
	h.SetNotifications(m)
	res, err := h.Dispatch(context.Background(), "app_exec", map[string]any{"script": `emit(notify_when_connected(device="iPad", webhook_url=params["webhook_url"]))`, "params": map[string]any{"webhook_url": "https://example.com/private"}})
	if err != nil || res.IsError {
		t.Fatalf("app_exec notification: %v %+v", err, res)
	}
	rows := m.List()
	if len(rows) != 1 || rows[0].DeviceID != "00008103-001122334455667A" || rows[0].State != "waiting" {
		t.Fatalf("offline identity: %+v", rows)
	}
	listed, err := h.Dispatch(context.Background(), "notifications", nil)
	if err != nil || listed.IsError {
		t.Fatalf("list: %v %+v", err, listed)
	}
	cancelled, err := h.Dispatch(context.Background(), "notification_cancel", map[string]any{"id": rows[0].ID})
	if err != nil || cancelled.IsError || m.List()[0].State != "cancelled" {
		t.Fatalf("cancel: %v %+v", err, cancelled)
	}
	for _, args := range []map[string]any{
		{"device": "typo", "webhook_url": "https://example.com/hook"},
		{"device": "iPad", "platform": "android", "webhook_url": "https://example.com/hook"},
		{"device": "12345678-1234-1234-1234-123456789abc", "platform": "ios", "webhook_url": "https://example.com/hook", "ttl": "bad"},
		{"device": "unknown", "platform": "desktop", "webhook_url": "https://example.com/hook"},
		{"device": "iPad", "ttl": "0s", "webhook_url": "https://example.com/hook"},
	} {
		r, err := h.Dispatch(context.Background(), "notify_when_connected", args)
		if err == nil && !r.IsError {
			t.Fatalf("invalid registration accepted: %+v", args)
		}
	}
}

func TestNotificationSourceWiring(t *testing.T) {
	t.Setenv(paths.DataDirEnv, t.TempDir())
	received := make(chan notify.Event, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e notify.Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
		}
		received <- e
		w.WriteHeader(204)
	}))
	defer server.Close()
	m, err := notify.Open(filepath.Join(t.TempDir(), "notifications.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Subscribe(notify.SubscribeRequest{Events: []string{"*"}, Recurring: true, WebhookURL: server.URL}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(&stubAdapter{}, &stubAdapter{})
	h.appChannel = appchannel.NewManager()
	defer h.appChannel.Close()
	h.reservations, err = reservations.New("")
	if err != nil {
		t.Fatal(err)
	}
	h.SetNotifications(m)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	if _, err := h.reservations.Acquire("phone", "test", time.Minute, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.reservations.Release("phone", "test"); err != nil {
		t.Fatal(err)
	}
	h.health.Model().Observe(health.ID{Kind: health.KindHost, Name: "test-network"}, false, "test failure")
	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{DeviceID: "phone", BundleID: "com.example.game"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(l.Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello, err := appchannel.PackParams(appchannel.Hello{AppName: "notification-test", AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: hello}); err != nil {
		t.Fatal(err)
	}
	if _, err := appchannel.ReadFrame(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	wf, err := verify.Load([]byte("name: event-wiring\nsteps:\n  - id: check\n    type: shell\n    argv: [/usr/bin/true]\n"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.VerifyHub().Start(ctx, verify.StartArgs{Workflow: wf, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if run.Wait().Status != verify.StatusPassed {
		t.Fatal("verification failed")
	}
	r, err := h.Dispatch(ctx, "app_exec", map[string]any{"script": `event_publish(event="custom.build.ready", data={"artifact":"fixture"})`})
	if err != nil || r.IsError {
		t.Fatalf("publish: %v %+v", err, r)
	}
	want := map[string]bool{"reservation.acquired": true, "reservation.released": true, "health.changed": true, "app.connected": true, "app.disconnected": true, "verify.started": true, "verify.completed": true, "custom.build.ready": true}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(want) > 0 {
		select {
		case e := <-received:
			if e.EventID == "" || e.OccurredAt.IsZero() {
				t.Fatalf("missing envelope identity: %+v", e)
			}
			if e.Event == "verify.completed" && e.Data["status"] != "passed" {
				t.Fatalf("wrong outcome: %+v", e)
			}
			delete(want, e.Event)
		case <-deadline.C:
			t.Fatalf("unwired events: %v", want)
		}
	}
}
