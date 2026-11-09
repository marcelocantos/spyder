// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package notify delivers durable event subscriptions and device callbacks.
package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	DefaultTTL         = 24 * time.Hour
	MaxTTL             = 7 * 24 * time.Hour
	MaxWebhookURLBytes = 8192
	maxActive          = 128
	deliveryTimeout    = 10 * time.Second
	maxBackoff         = 30 * time.Second
)

// Subscription omits the callback URL: T3 webhook URLs are bearer credentials.
type Subscription struct {
	Events      []string          `json:"events,omitempty"`
	Match       map[string]string `json:"match,omitempty"`
	Recurring   bool              `json:"recurring,omitempty"`
	Pending     int               `json:"pending,omitempty"`
	Delivered   int               `json:"delivered,omitempty"`
	Dropped     int               `json:"dropped,omitempty"`
	LastEventID string            `json:"last_event_id,omitempty"`
	ID          string            `json:"id"`
	Event       string            `json:"event"`
	Device      string            `json:"device"`
	DeviceID    string            `json:"device_id"`
	Platform    string            `json:"platform"`
	State       string            `json:"state"`
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
	ConnectedAt *time.Time        `json:"connected_at,omitempty"`
	DeliveredAt *time.Time        `json:"delivered_at,omitempty"`
	Attempts    int               `json:"attempts"`
	LastError   string            `json:"last_error,omitempty"`
}

type record struct {
	Subscription
	WebhookURL string  `json:"webhook_url"`
	Queue      []Event `json:"pending_events,omitempty"`
}

// Event is the callback body. EventID is stable across retries and restarts;
// configure T3's replayKey as {{body.event_id}} for duplicate suppression.
type Event struct {
	OccurredAt  time.Time         `json:"occurred_at,omitzero"`
	Data        map[string]string `json:"data,omitempty"`
	EventID     string            `json:"event_id"`
	Event       string            `json:"event"`
	Device      string            `json:"device"`
	DeviceID    string            `json:"device_id"`
	Platform    string            `json:"platform"`
	ConnectedAt time.Time         `json:"connected_at,omitzero"`
}

type Request struct {
	Device     string
	DeviceID   string
	Platform   string
	WebhookURL string
	TTL        time.Duration
}

// WaitConnected returns when the named device is connected, including when
// it is already present. It must return promptly when ctx is cancelled.
type WaitConnected func(ctx context.Context, platform, id string) error

type Manager struct {
	mu         sync.Mutex
	path       string
	records    map[string]record
	jobs       map[string]context.CancelFunc
	wake       chan struct{}
	notices    map[string]chan struct{}
	sourceWake chan struct{}
	wait       WaitConnected
	client     *http.Client
}

func Open(path string, wait WaitConnected) (*Manager, error) {
	m := &Manager{path: path, records: map[string]record{}, jobs: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1), notices: map[string]chan struct{}{}, sourceWake: make(chan struct{}, 1), wait: wait,
		client: &http.Client{Timeout: deliveryTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &m.records); err != nil {
			return nil, fmt.Errorf("decode notifications: %w", err)
		}
		if m.records == nil {
			return nil, errors.New("invalid notifications store")
		}
		for id, r := range m.records {
			if id != r.ID || r.ExpiresAt.IsZero() || (len(r.Events) == 0 && (r.Event != "device.connected" || r.DeviceID == "")) {
				return nil, errors.New("invalid notification record")
			}
			switch r.State {
			case "waiting", "ready":
				if r.WebhookURL == "" || (len(r.Events) == 0 && ((r.Platform != "ios" && r.Platform != "android") || (r.State == "ready" && r.ConnectedAt == nil))) {
					return nil, errors.New("invalid pending notification")
				}
				if err := validateCallback(r.WebhookURL, DefaultTTL); err != nil {
					return nil, errors.New("invalid stored notification callback")
				}
			case "delivered", "cancelled", "expired":
			default:
				return nil, errors.New("invalid notification state")
			}
			if len(r.Events) > 0 {
				if len(r.Queue) > maxPendingEvents {
					return nil, errors.New("invalid stored notification queue")
				}
				for _, e := range r.Queue {
					if e.EventID == "" || !knownEvent(e.Event) {
						return nil, errors.New("invalid stored event")
					}
				}
				r.Pending = len(r.Queue)
				m.records[id] = r
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Add(r Request) (Subscription, error) {
	if r.Platform != "ios" && r.Platform != "android" {
		return Subscription{}, errors.New("notifications require an ios or android device")
	}
	if r.DeviceID == "" {
		return Subscription{}, errors.New("device identifier is empty")
	}
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
	for _, s := range m.records {
		if (s.State == "waiting" || s.State == "ready") && now.Before(s.ExpiresAt) {
			active++
			if len(s.Events) == 0 && s.DeviceID == r.DeviceID && s.Platform == r.Platform && s.WebhookURL == r.WebhookURL {
				return s.Subscription, nil
			}
		}
	}
	if active >= maxActive {
		return Subscription{}, fmt.Errorf("too many active notifications (maximum %d)", maxActive)
	}
	id := rand.Text()
	s := record{Subscription: Subscription{ID: id, Event: "device.connected", Device: r.Device, DeviceID: r.DeviceID, Platform: r.Platform, State: "waiting", CreatedAt: now, ExpiresAt: now.Add(r.TTL)}, WebhookURL: r.WebhookURL}
	m.records[id] = s
	if err := m.saveLocked(); err != nil {
		delete(m.records, id)
		return Subscription{}, err
	}
	m.signal()
	return s.Subscription, nil
}

func validateCallback(webhook string, ttl time.Duration) error {
	if len(webhook) > MaxWebhookURLBytes {
		return errors.New("webhook URL too long")
	}
	u, err := url.Parse(webhook)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return errors.New("webhook_url must be HTTPS (HTTP allowed on loopback), without user info or fragment")
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < time.Second || ttl > MaxTTL {
		return errors.New("ttl must be between 1 second and 7 days")
	}
	return nil
}

func (m *Manager) List() []Subscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Subscription, 0, len(m.records))
	for _, s := range m.records {
		view := s.Subscription
		view.Events = slices.Clone(view.Events)
		view.Match = maps.Clone(view.Match)
		out = append(out, view)
	}
	slices.SortFunc(out, func(a, b Subscription) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// Cancel stops waiting/retries. An HTTP request already accepted by T3 cannot
// be recalled; callers must also remove the webhook task if no longer needed.
func (m *Manager) Cancel(id string) (Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.records[id]
	if !ok {
		return Subscription{}, errors.New("notification not found")
	}
	if s.State != "waiting" && s.State != "ready" {
		return s.Subscription, nil
	}
	previous := s
	s.State = "cancelled"
	s.WebhookURL = ""
	s.Queue = nil
	s.Pending = 0
	m.records[id] = s
	if err := m.saveLocked(); err != nil {
		m.records[id] = previous
		return Subscription{}, err
	}
	if cancel := m.jobs[id]; cancel != nil {
		cancel()
	}
	return s.Subscription, nil
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
	select {
	case m.sourceWake <- struct{}{}:
	default:
	}
}

// Run owns all wait and delivery jobs and joins them on shutdown. Each device
// has its own cancellable wait, so a stuck provider or callback cannot hold up
// another device. Delivery is at least once until expiry; no network I/O holds mu.
func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer func() {
		m.mu.Lock()
		for _, cancel := range m.jobs {
			cancel()
		}
		m.mu.Unlock()
		wg.Wait()
	}()
	for {
		m.mu.Lock()
		for id, s := range m.records {
			if s.State != "waiting" && s.State != "ready" {
				continue
			}
			if _, running := m.jobs[id]; running {
				continue
			}
			jobCtx, cancel := context.WithDeadline(ctx, s.ExpiresAt)
			m.jobs[id] = cancel
			notice := make(chan struct{}, 1)
			m.notices[id] = notice
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer cancel()
				if len(s.Events) > 0 {
					m.runSubscription(jobCtx, id, notice)
				} else {
					m.runJob(jobCtx, id)
				}
				m.mu.Lock()
				delete(m.jobs, id)
				delete(m.notices, id)
				m.mu.Unlock()
			}()
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-tick.C:
		}
	}
}

func (m *Manager) runJob(ctx context.Context, id string) {
	backoff := time.Second
	for ctx.Err() == nil {
		m.mu.Lock()
		s := m.records[id]
		m.mu.Unlock()
		if s.State != "waiting" && s.State != "ready" {
			return
		}
		if s.State == "waiting" {
			if err := m.wait(ctx, s.Platform, s.DeviceID); err != nil {
				if saveErr := m.change(id, func(r *record) { r.LastError = "device connection event source unavailable; retrying" }); saveErr != nil {
					slog.Error("notification state save failed", "id", id, "error", saveErr)
				}
				if !pause(ctx, backoff) {
					break
				}
				backoff = min(backoff*2, maxBackoff)
				continue
			}
			now := time.Now().UTC()
			if err := m.change(id, func(r *record) {
				if r.State == "waiting" {
					r.State = "ready"
					r.ConnectedAt = &now
					r.LastError = ""
				}
			}); err != nil {
				if !pause(ctx, backoff) {
					break
				}
				continue
			}
			backoff = time.Second
			continue
		}
		if err := m.change(id, func(r *record) { r.Attempts++ }); err != nil {
			if !pause(ctx, backoff) {
				break
			}
			continue
		}
		// Recheck cancellation after saving, before starting a callback.
		if ctx.Err() != nil {
			break
		}
		err := m.deliver(ctx, s)
		if err == nil {
			now := time.Now().UTC()
			if err = m.change(id, func(r *record) { r.State = "delivered"; r.DeliveredAt = &now; r.LastError = ""; r.WebhookURL = "" }); err == nil {
				return
			}
		} else {
			if saveErr := m.change(id, func(r *record) { r.LastError = err.Error() }); saveErr != nil {
				slog.Error("notification state save failed", "id", id, "error", saveErr)
			}
		}
		if !pause(ctx, backoff) {
			break
		}
		backoff = min(backoff*2, maxBackoff)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if err := m.change(id, func(r *record) { r.State = "expired"; r.WebhookURL = "" }); err != nil {
			slog.Error("notification expiry save failed", "id", id, "error", err)
		}
	}
}

func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (m *Manager) change(id string, update func(*record)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.records[id]
	if old.State != "waiting" && old.State != "ready" {
		return nil
	}
	next := old
	update(&next)
	m.records[id] = next
	if err := m.saveLocked(); err != nil {
		m.records[id] = old
		return err
	}
	return nil
}

func (m *Manager) deliver(ctx context.Context, s record) error {
	return m.deliverEvent(ctx, s.WebhookURL, Event{EventID: s.ID, Event: s.Event, Device: s.Device, DeviceID: s.DeviceID, Platform: s.Platform, ConnectedAt: *s.ConnectedAt, OccurredAt: *s.ConnectedAt})
}

func (m *Manager) deliverEvent(ctx context.Context, webhook string, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid callback request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Spyder-Event-ID", event.EventID)
	res, err := m.client.Do(req)
	if err != nil {
		return errors.New("callback transport failed (URL redacted)")
	}
	defer res.Body.Close()
	// Do not record response bodies: a provider may echo URL credentials.
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("callback HTTP %d", res.StatusCode)
	}
	return nil
}

func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m.records, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), ".notifications-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), m.path)
}
