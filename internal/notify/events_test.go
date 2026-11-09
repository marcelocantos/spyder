// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenericRecurringQueuesDuringDeliveryAndFilters(t *testing.T) {
	events := make(chan Event, 8)
	blocked := make(chan struct{})
	defer close(blocked)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
		}
		events <- e
		if e.EventID == "first" {
			select {
			case <-blocked:
			case <-r.Context().Done():
			}
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	m, err := Open(filepath.Join(t.TempDir(), "notifications.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Subscribe(SubscribeRequest{Events: []string{"app.*"}, Match: map[string]string{"bundle_id": "com.example.game"}, Recurring: true, WebhookURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	stop := startManager(t, m)
	defer stop()
	for _, e := range []Event{
		{Event: "app.connected", EventID: "wrong", Data: map[string]string{"bundle_id": "other"}},
		{Event: "app.connected", EventID: "missing"},
		{Event: "app.connected", EventID: "first", Data: map[string]string{"bundle_id": "com.example.game"}},
		{Event: "app.disconnected", EventID: "second", Data: map[string]string{"bundle_id": "com.example.game"}},
	} {
		if err := m.Publish(e); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case e := <-events:
		if e.EventID != "first" {
			t.Fatalf("filter mismatch: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no delivery")
	}
	if rows := m.List(); rows[0].Pending != 2 {
		t.Fatalf("delivery lost queued events: %+v", rows)
	}
	// Cancel while callback is in flight; the second event must remain unsent.
	if _, err := m.Cancel(s.ID); err != nil {
		t.Fatal(err)
	}
	awaitState(t, m, s.ID, "cancelled")
	stop()
	select {
	case e := <-events:
		t.Fatalf("callback after cancel: %+v", e)
	default:
	}
}

func TestGenericOutboxRestartAndOneShot(t *testing.T) {
	events := make(chan Event, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
		}
		events <- e
		w.WriteHeader(204)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "notifications.json")
	m, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	one, err := m.Subscribe(SubscribeRequest{Events: []string{"verify.completed"}, Match: map[string]string{"status": "failed"}, WebhookURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := m.Subscribe(SubscribeRequest{Events: []string{"verify.*"}, Recurring: true, WebhookURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Event{
		{Event: "verify.started", EventID: "started"},
		{Event: "verify.completed", EventID: "failed-1", Data: map[string]string{"status": "failed"}},
		{Event: "verify.completed", EventID: "failed-2", Data: map[string]string{"status": "failed"}},
	} {
		if err := m.Publish(e); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	startManager(t, restored)
	awaitState(t, restored, one.ID, "delivered")
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var received []string
	for range 4 {
		select {
		case e := <-events:
			received = append(received, e.EventID)
		case <-deadline.C:
			t.Fatalf("lost persisted events: %v", received)
		}
	}
	counts := map[string]int{}
	for _, id := range received {
		counts[id]++
	}
	if counts["started"] != 1 || counts["failed-1"] != 2 || counts["failed-2"] != 1 {
		t.Fatalf("one-shot/fanout mismatch: %v", counts)
	}
	for _, row := range restored.List() {
		if row.ID == repeat.ID && row.Dropped != 0 {
			t.Fatalf("unexpected drops: %+v", row)
		}
	}
}

func TestGenericOverflowAndValidation(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "notifications.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []SubscribeRequest{
		{Events: []string{"device.conected"}},
		{Events: []string{"app.connected"}, Match: map[string]string{"bundel_id": "typo"}},
		{Events: []string{"unknown.*"}},
		{Events: []string{"custom." + strings.Repeat("a", maxEventNameBytes)}},
	} {
		r.WebhookURL = "https://example.com/hook"
		if _, err := m.Subscribe(r); err == nil {
			t.Fatalf("invalid request accepted: %+v", r)
		}
	}
	s, err := m.Subscribe(SubscribeRequest{Events: []string{"custom.*"}, Recurring: true, WebhookURL: "https://example.com/hook"})
	if err != nil {
		t.Fatal(err)
	}
	for range maxPendingEvents {
		if err := m.Publish(Event{Event: "custom.build.ready"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Publish(Event{Event: "custom.build.ready"}); err == nil {
		t.Fatal("overflow not reported")
	}
	row := m.List()[0]
	if row.ID != s.ID || row.Pending != maxPendingEvents || row.Dropped != 1 {
		t.Fatalf("overflow status wrong: %+v", row)
	}
	if err := m.Publish(Event{Event: "custom.build.ready", Data: map[string]string{"event_id": "spoof"}}); err == nil {
		t.Fatal("reserved fields accepted")
	}
}

func TestLegacyStateLoadsAlongsideGenericAndFailedEnqueueRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	m, err := Open(path, func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	old := addNotification(t, m, "https://example.com/old", "serial")
	s, err := m.Subscribe(SubscribeRequest{Events: []string{"health.changed"}, WebhookURL: "https://example.com/new"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.List()) != 2 {
		t.Fatal("old state lost")
	}
	block := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(block, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	restored.path = filepath.Join(block, "notifications.json")
	if err := restored.Publish(Event{Event: "health.changed"}); err == nil {
		t.Fatal("save failure ignored")
	}
	for _, row := range restored.List() {
		if row.ID == s.ID && row.Pending != 0 {
			t.Fatal("unsaved event retained")
		}
		if row.ID == old.ID && row.Event != "device.connected" {
			t.Fatal("legacy state changed")
		}
	}
}

func TestADBTrackerFramingAndAuthorizedTransitions(t *testing.T) {
	var stream strings.Builder
	for _, snapshot := range []string{"a\tunauthorized\nb\tdevice\n", "a\tdevice\nb\tdevice\n", "a\toffline\n", ""} {
		fmt.Fprintf(&stream, "%04x%s", len(snapshot), snapshot)
	}
	var events []Event
	err := readADBEvents(strings.NewReader(stream.String()), func(e Event) { events = append(events, e) })
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []string{"device.connected:b", "device.connected:a", "device.disconnected:a", "device.disconnected:b"}
	if len(events) != len(want) {
		t.Fatalf("bad transitions: %+v", events)
	}
	// The two removals share a snapshot, so map iteration may reverse them.
	if events[0].Event+":"+events[0].DeviceID != want[0] || events[1].Event+":"+events[1].DeviceID != want[1] {
		t.Fatalf("unauthorized admitted: %+v", events)
	}
	for _, e := range events[2:] {
		if e.Event != "device.disconnected" {
			t.Fatalf("bad removal: %+v", e)
		}
	}
	if err := readADBEvents(strings.NewReader("oops"), func(Event) {}); err == nil {
		t.Fatal("invalid framing accepted")
	}
}
