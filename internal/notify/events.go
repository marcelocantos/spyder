// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
)

const (
	maxPendingEvents   = 256
	maxEventFields     = 32
	maxEventValueBytes = 4096
	maxEventNames      = 16
	maxEventNameBytes  = 128
	maxEventDataBytes  = 16 * 1024
)

type EventType struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Fields      []string `json:"fields"`
}

// EventTypes is the discoverable contract. Add a source here and publish from
// its actual lifecycle boundary; delivery and filtering need no source code.
func EventTypes() []EventType {
	return []EventType{
		{"device.connected", "Transport connected (Android authorized); startup sightings included", []string{"device", "device_id", "platform"}},
		{"device.disconnected", "Previously observed transport disconnected", []string{"device", "device_id", "platform"}},
		{"app.connected", "App-channel hello accepted", []string{"device", "device_id", "bundle_id", "session_id", "owner"}},
		{"app.disconnected", "Accepted app-channel session ended", []string{"device", "device_id", "bundle_id", "session_id", "owner"}},
		{"reservation.acquired", "Device reservation acquired", []string{"device", "device_id", "owner"}},
		{"reservation.renewed", "Device reservation renewed", []string{"device", "device_id", "owner"}},
		{"reservation.released", "Held device released", []string{"device", "device_id", "owner"}},
		{"reservation.expired", "Reservation expiry observed by the store", []string{"device", "device_id", "owner"}},
		{"health.changed", "Entity health state changed", []string{"kind", "name", "layer", "from", "state"}},
		{"verify.started", "Verification workflow started", []string{"run_id", "workflow", "owner", "device"}},
		{"verify.completed", "Verification workflow finished", []string{"run_id", "workflow", "owner", "device", "status"}},
		{"verify.waiting", "Verification step requires human input", []string{"run_id", "workflow", "owner", "device", "step_id"}},
		{"custom.*", "Explicit events published by scripts/workflows; use a named custom topic", nil},
	}
}

func knownEvent(name string) bool {
	if len(name) > maxEventNameBytes {
		return false
	}
	if strings.HasPrefix(name, "custom.") && len(name) > len("custom.") {
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				return false
			}
		}
		return true
	}
	return slices.ContainsFunc(EventTypes(), func(t EventType) bool { return t.Name == name })
}

func eventMatches(pattern, name string) bool {
	return pattern == name || pattern == "*" || strings.HasSuffix(pattern, ".*") && strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
}

type SubscribeRequest struct {
	Events     []string
	Match      map[string]string
	Recurring  bool
	WebhookURL string
	TTL        time.Duration
}

func (m *Manager) Subscribe(r SubscribeRequest) (Subscription, error) {
	if len(r.Events) == 0 || len(r.Events) > maxEventNames {
		return Subscription{}, fmt.Errorf("events must contain 1..%d event names or family patterns", maxEventNames)
	}
	for _, name := range r.Events {
		valid := knownEvent(name)
		if name == "*" {
			valid = true
		}
		if strings.HasSuffix(name, ".*") {
			valid = slices.ContainsFunc(EventTypes(), func(t EventType) bool { return eventMatches(name, t.Name) })
		}
		if !valid {
			return Subscription{}, fmt.Errorf("unknown event %q; inspect notification_events", name)
		}
	}
	if len(r.Match) > maxEventFields {
		return Subscription{}, errors.New("too many match fields")
	}
	for key, value := range r.Match {
		valid := false
		for _, name := range r.Events {
			if name == "*" || strings.HasPrefix(name, "custom.") {
				valid = true
			}
			for _, t := range EventTypes() {
				if eventMatches(name, t.Name) && slices.Contains(t.Fields, key) {
					valid = true
				}
			}
		}
		if !valid || len(key) > maxEventValueBytes || len(value) > maxEventValueBytes {
			return Subscription{}, fmt.Errorf("invalid match field %q for requested events", key)
		}
	}
	// Reuse the callback/TTL validation, without registering a legacy wait.
	if err := validateCallback(r.WebhookURL, r.TTL); err != nil {
		return Subscription{}, err
	}
	if r.TTL == 0 {
		r.TTL = DefaultTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	active := 0
	events := slices.Clone(r.Events)
	slices.Sort(events)
	events = slices.Compact(events)
	for _, s := range m.records {
		if (s.State == "waiting" || s.State == "ready") && now.Before(s.ExpiresAt) {
			active++
			if slices.Equal(s.Events, events) && maps.Equal(s.Match, r.Match) && s.Recurring == r.Recurring && s.WebhookURL == r.WebhookURL {
				return s.Subscription, nil
			}
		}
	}
	if active >= maxActive {
		return Subscription{}, fmt.Errorf("too many active notifications (maximum %d)", maxActive)
	}
	id := rand.Text()
	s := record{Subscription: Subscription{ID: id, Event: "subscription", Events: events, Match: maps.Clone(r.Match), Recurring: r.Recurring, State: "waiting", CreatedAt: now, ExpiresAt: now.Add(r.TTL)}, WebhookURL: r.WebhookURL}
	m.records[id] = s
	if err := m.saveLocked(); err != nil {
		delete(m.records, id)
		return Subscription{}, err
	}
	m.signal()
	return s.Subscription, nil
}

// Publish atomically queues an event for all matching active subscriptions.
// No network call runs on the publisher. Overflow is explicit in status/error;
// accepted queued events retain their IDs across retries and daemon restarts.
func (m *Manager) Publish(event Event) error {
	if !knownEvent(event.Event) {
		return errors.New("unknown event type")
	}
	if len(event.Data) > maxEventFields {
		return errors.New("too many event fields")
	}
	for _, v := range []string{event.Event, event.EventID, event.Device, event.DeviceID, event.Platform} {
		if len(v) > maxEventValueBytes {
			return errors.New("event identity too long")
		}
	}
	size := 0
	for k, v := range event.Data {
		switch k {
		case "event", "event_id", "occurred_at", "device", "device_id", "platform", "connected_at":
			return errors.New("event data cannot shadow envelope fields")
		}
		if len(k) > maxEventValueBytes || len(v) > maxEventValueBytes {
			return errors.New("event field too long")
		}
		size += len(k) + len(v)
	}
	if size > maxEventDataBytes {
		return errors.New("event data exceeds 16 KiB")
	}
	event.Data = maps.Clone(event.Data)
	if event.EventID == "" {
		event.EventID = rand.Text()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := maps.Clone(m.records)
	changed := false
	overflow := false
	for id, s := range m.records {
		if len(s.Events) == 0 || (s.State != "waiting" && s.State != "ready") || !time.Now().Before(s.ExpiresAt) {
			continue
		}
		if !slices.ContainsFunc(s.Events, func(p string) bool { return eventMatches(p, event.Event) }) {
			continue
		}
		matched := true
		for key, value := range s.Match {
			if got, ok := event.field(key); !ok || got != value {
				matched = false
				break
			}
		}
		if !matched || !s.Recurring && len(s.Queue) > 0 {
			continue
		}
		if len(s.Queue) >= maxPendingEvents {
			s.Dropped++
			s.LastError = "event queue full; see dropped count"
			overflow = true
		} else {
			s.Queue = append(slices.Clone(s.Queue), event)
			s.Pending = len(s.Queue)
			s.State = "ready"
		}
		m.records[id] = s
		changed = true
	}
	if changed {
		if err := m.saveLocked(); err != nil {
			m.records = previous
			return err
		}
		for _, ch := range m.notices {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		m.signal()
	}
	if overflow {
		return errors.New("notification event queue full; dropped count incremented")
	}
	return nil
}

func (e Event) field(key string) (string, bool) {
	switch key {
	case "device":
		return e.Device, e.Device != ""
	case "device_id":
		return e.DeviceID, e.DeviceID != ""
	case "platform":
		return e.Platform, e.Platform != ""
	default:
		v, ok := e.Data[key]
		return v, ok
	}
}

func (m *Manager) runSubscription(ctx context.Context, id string, notice <-chan struct{}) {
	backoff := time.Second
	for ctx.Err() == nil {
		m.mu.Lock()
		s := m.records[id]
		m.mu.Unlock()
		if s.State != "waiting" && s.State != "ready" {
			return
		}
		if len(s.Queue) == 0 {
			select {
			case <-ctx.Done():
			case <-notice:
			}
			continue
		}
		if err := m.change(id, func(r *record) { r.Attempts++ }); err != nil {
			if !pause(ctx, backoff) {
				break
			}
			continue
		}
		err := m.deliverEvent(ctx, s.WebhookURL, s.Queue[0])
		if err == nil {
			now := time.Now().UTC()
			err = m.change(id, func(r *record) {
				r.LastEventID = s.Queue[0].EventID
				r.Queue = slices.Clone(r.Queue[1:])
				r.Pending = len(r.Queue)
				r.Delivered++
				r.DeliveredAt = &now
				r.LastError = ""
				if !r.Recurring {
					r.State = "delivered"
					r.WebhookURL = ""
				} else if len(r.Queue) == 0 {
					r.State = "waiting"
				}
			})
			if err == nil {
				backoff = time.Second
				continue
			}
		} else {
			if saveErr := m.change(id, func(r *record) { r.LastError = err.Error() }); saveErr != nil {
				slog.Error("notification save failed", "id", id, "error", saveErr)
			}
		}
		if !pause(ctx, backoff) {
			break
		}
		backoff = min(backoff*2, maxBackoff)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if err := m.change(id, func(r *record) { r.State = "expired"; r.WebhookURL = ""; r.Queue = nil; r.Pending = 0 }); err != nil {
			slog.Error("notification expiry save failed", "id", id, "error", err)
		}
	}
}
