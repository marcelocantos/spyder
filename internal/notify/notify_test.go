// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func startManager(t *testing.T, m *Manager) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("manager failed to stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func awaitState(t *testing.T, m *Manager, id, state string) Subscription {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		for _, s := range m.List() {
			if s.ID == id && s.State == state {
				return s
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("wanted %s, got %+v", state, m.List())
		case <-tick.C:
		}
	}
}

func addNotification(t *testing.T, m *Manager, url, id string) Subscription {
	t.Helper()
	s, err := m.Add(Request{Device: "phone", DeviceID: id, Platform: "android", WebhookURL: url, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConnectionRetriesStableEventAndRedactsCredentials(t *testing.T) {
	connected := make(chan struct{})
	var calls atomic.Int32
	events := make(chan Event, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Error(err)
		}
		if r.Header.Get("X-Spyder-Event-ID") != ev.EventID || r.Header.Get("Content-Type") != "application/json" {
			t.Error("callback headers missing")
		}
		events <- ev
		if calls.Add(1) == 1 {
			http.Error(w, "private-token", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "notifications.json")
	m, err := Open(path, func(ctx context.Context, platform, id string) error {
		if platform != "android" || id != "serial" {
			t.Errorf("wrong identity %s %s", platform, id)
		}
		select {
		case <-connected:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	startManager(t, m)
	s := addNotification(t, m, srv.URL+"/private-token", "serial")
	duplicate := addNotification(t, m, srv.URL+"/private-token", "serial")
	if duplicate.ID != s.ID {
		t.Fatal("duplicate registration")
	}
	if calls.Load() != 0 {
		t.Fatal("callback before connection")
	}
	close(connected)
	delivered := awaitState(t, m, s.ID, "delivered")
	if delivered.Attempts != 2 || delivered.ConnectedAt == nil || delivered.DeliveredAt == nil {
		t.Fatalf("bad delivery: %+v", delivered)
	}
	one, two := <-events, <-events
	if one != two || one.EventID != s.ID || one.Event != "device.connected" {
		t.Fatalf("unstable payload: %+v %+v", one, two)
	}
	b, _ := json.Marshal(m.List())
	if strings.Contains(string(b), "private-token") || strings.Contains(string(b), "webhook_url") {
		t.Fatal("credentials leaked into status")
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "private-token") {
		t.Fatal("delivered secret retained")
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("private permissions: %v %v", fi, err)
	}
}

func TestRestartKeepsObservedConnectionWithoutWaitingAgain(t *testing.T) {
	var accept atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !accept.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "notifications.json")
	m, err := Open(path, func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	s := addNotification(t, m, srv.URL, "serial")
	stop := startManager(t, m)
	// Once ready is persisted, it must not need another physical attachment.
	awaitState(t, m, s.ID, "ready")
	stop()
	accept.Store(true)
	var waited atomic.Bool
	restarted, err := Open(path, func(context.Context, string, string) error { waited.Store(true); return errors.New("device is gone") })
	if err != nil {
		t.Fatal(err)
	}
	startManager(t, restarted)
	awaitState(t, restarted, s.ID, "delivered")
	if waited.Load() {
		t.Fatal("observed event lost on restart")
	}
}

func TestCancelAndExpiryStopDeviceWait(t *testing.T) {
	started := make(chan string, 2)
	stopped := make(chan string, 2)
	m, err := Open(filepath.Join(t.TempDir(), "notifications.json"), func(ctx context.Context, _, id string) error {
		started <- id
		<-ctx.Done()
		stopped <- id
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	s := addNotification(t, m, "http://127.0.0.1:1/private", "cancel")
	expiring, err := m.Add(Request{DeviceID: "expire", Platform: "ios", WebhookURL: "http://127.0.0.1:1/private", TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	startManager(t, m)
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("wait not started")
		}
	}
	if _, err := m.Cancel(s.ID); err != nil {
		t.Fatal(err)
	}
	awaitState(t, m, s.ID, "cancelled")
	awaitState(t, m, expiring.ID, "expired")
	for range 2 {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("wait leaked")
		}
	}
	if _, err := m.Cancel("missing"); err == nil {
		t.Fatal("missing id accepted")
	}
}

func TestSlowDeviceCannotBlockAnotherNotification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	m, err := Open(filepath.Join(t.TempDir(), "notifications.json"), func(ctx context.Context, _, id string) error {
		if id == "slow" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	addNotification(t, m, srv.URL, "slow")
	fast := addNotification(t, m, srv.URL, "fast")
	startManager(t, m)
	awaitState(t, m, fast.ID, "delivered")
}

func TestRejectUnsafeURLAndRedirectWithoutLeakingSecrets(t *testing.T) {
	var destinationCalls atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls.Add(1) }))
	defer dest.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+"/private", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	m, err := Open(filepath.Join(t.TempDir(), "notifications.json"), func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"http://example.com/private", "file:///private", "https://user:private@example.com", "https://example.com/#private", ":bad%private"} {
		_, err := m.Add(Request{DeviceID: "phone", Platform: "ios", WebhookURL: url})
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Errorf("bad URL error: %v", err)
		}
	}
	s := addNotification(t, m, srv.URL, "serial")
	startManager(t, m)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		rows := m.List()
		if rows[0].LastError != "" {
			if rows[0].LastError != "callback HTTP 307" || destinationCalls.Load() != 0 {
				t.Fatalf("redirect followed: %+v", rows)
			}
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("no callback attempt")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := m.Cancel(s.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFailedSaveDoesNotRegister(t *testing.T) {
	root := t.TempDir()
	block := filepath.Join(root, "file")
	if err := os.WriteFile(block, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Open(filepath.Join(root, "notifications.json"), func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.path = filepath.Join(block, "notifications.json")
	if _, err = m.Add(Request{DeviceID: "serial", Platform: "android", WebhookURL: "https://example.com/hook"}); err == nil {
		t.Fatal("save error ignored")
	}
	if len(m.List()) != 0 {
		t.Fatal("unsaved registration retained")
	}
}
