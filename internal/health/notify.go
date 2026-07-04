// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"fmt"
	"sync"
	"time"
)

// defaultNotifyCooldown is the minimum quiet period between firing a
// notification for the same entity after it was previously cleared.
// Five minutes is long enough to avoid re-notifying from a brief
// healthy blip but short enough that a genuine repeat fault still
// surfaces to the user.
const defaultNotifyCooldown = 5 * time.Minute

// Notifier delivers (or updates/dismisses) one actionable notification,
// keyed so repeats and clears target the same on-screen item. The macOS
// implementation shells to terminal-notifier (preferred) or osascript;
// tests use a recording fake. This interface is the ORACLE BOUNDARY:
// the fire/dedup/clear DECISION is class-1 testable against a fake, while
// real delivery is class-3 (confirmed once by eye).
type Notifier interface {
	// Notify posts or updates in-place the notification identified by key.
	Notify(key, title, message string) error
	// Clear dismisses the notification identified by key; no-op if absent.
	Clear(key string) error
}

// AttentionNotifier subscribes to a health.Model and pushes exactly one
// notification per entity that reaches NeedsAttention — deduped, cooldown-
// gated against flapping, and auto-cleared when the entity leaves
// NeedsAttention. Everything below NeedsAttention (healthy, degraded,
// recovering, absent_*) stays SILENT — it lives only in the pull surface
// (T90.3). The push bar is deliberately high: on a personal tool,
// notification fatigue destroys trust faster than a missed alert.
type AttentionNotifier struct {
	notifier Notifier
	model    *Model
	cooldown time.Duration
	now      func() time.Time

	mu        sync.Mutex
	active    map[ID]bool      // entities with a currently-posted notification
	lastFired map[ID]time.Time // for cooldown gating of repeats
}

// NotifyOption configures an AttentionNotifier at construction time.
type NotifyOption func(*AttentionNotifier)

// WithNotifyCooldown overrides the default cooldown between repeated
// notifications for the same entity. Use in tests to make the window
// controllable without sleeping.
func WithNotifyCooldown(d time.Duration) NotifyOption {
	return func(a *AttentionNotifier) {
		a.cooldown = d
	}
}

// WithNotifyClock injects a custom clock function. Use in tests to drive
// time forward deterministically without real sleeps.
func WithNotifyClock(fn func() time.Time) NotifyOption {
	return func(a *AttentionNotifier) {
		a.now = fn
	}
}

// NewAttentionNotifier constructs an AttentionNotifier that uses n for
// delivery. Call Attach(model) to wire up the transition observer.
func NewAttentionNotifier(n Notifier, opts ...NotifyOption) *AttentionNotifier {
	a := &AttentionNotifier{
		notifier:  n,
		cooldown:  defaultNotifyCooldown,
		now:       time.Now,
		active:    make(map[ID]bool),
		lastFired: make(map[ID]time.Time),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Attach registers the transition observer on m. Call once after
// constructing the notifier and the model.
func (a *AttentionNotifier) Attach(m *Model) {
	// Keep a reference so we can call Get on it during transitions.
	a.model = m
	m.OnTransition(a.onTransition)
}

// onTransition is the transition callback. It is called outside the
// model lock (per Model.fire contract), so it is safe to call model
// methods from here.
func (a *AttentionNotifier) onTransition(t Transition) {
	switch {
	case t.To == NeedsAttention:
		a.handleNeedsAttention(t)
	case t.From == NeedsAttention && t.To != NeedsAttention:
		a.handleCleared(t)
		// All other transitions (healthy↔degraded↔recovering, absent_*) are
		// intentionally silent — they belong to the pull surface only (T90.3).
	}
}

// handleNeedsAttention fires a notification for t.ID, subject to dedup
// and cooldown guards.
//
// The whole check → snapshot → notify → mark-active sequence runs under
// a.mu so two concurrent NeedsAttention transitions for the same entity
// can't both pass the dedup check and double-fire. Holding a.mu across
// model.Get is safe: Model.fire invokes observers with the model lock
// RELEASED, so an observer re-entering the model (Get) never closes a
// lock-ordering cycle.
func (a *AttentionNotifier) handleNeedsAttention(t Transition) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.active[t.ID] {
		return // notification already on screen; dedup
	}
	if fired, ok := a.lastFired[t.ID]; ok && a.now().Sub(fired) < a.cooldown {
		return // cooldown suppresses a rapid repeat after a clear (anti-flap)
	}

	snap, ok := a.model.Get(t.ID)
	if !ok {
		return // entity disappeared between the transition and our Get
	}
	title, message := buildMessage(snap)

	//nolint:errcheck // delivery is best-effort; a missing binary must not crash the daemon
	_ = a.notifier.Notify(notifyKey(t.ID), title, message)
	a.active[t.ID] = true
	a.lastFired[t.ID] = a.now()
}

// handleCleared dismisses the on-screen notification for t.ID. Runs under
// a.mu (safe per the note on handleNeedsAttention) so a clear and a
// concurrent re-fire can't interleave into an inconsistent active set.
func (a *AttentionNotifier) handleCleared(t Transition) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active[t.ID] {
		return // nothing on screen to clear
	}
	delete(a.active, t.ID)
	// Keep lastFired so the cooldown still gates a rapid re-fire.

	//nolint:errcheck // best-effort; see above
	_ = a.notifier.Clear(notifyKey(t.ID))
}

// notifyKey returns a stable string key for an entity, used to identify
// a notification across post/update/dismiss calls (e.g. the -group flag
// in terminal-notifier). Format: "<kind>/<name>[/<layer>]".
func notifyKey(id ID) string {
	if id.Layer != "" {
		return fmt.Sprintf("%s/%s/%s", id.Kind, id.Name, id.Layer)
	}
	return fmt.Sprintf("%s/%s", id.Kind, id.Name)
}

// buildMessage renders an actionable, specific notification for an entity
// in NeedsAttention: names the entity and the recovery step, using the
// latest evidence Detail for specifics. Never a generic "something failed".
func buildMessage(snap EntitySnapshot) (title, message string) {
	// latestDetail extracts the Detail from the most-recent evidence
	// observation, or returns an empty string if there is none.
	latestDetail := func() string {
		if len(snap.Evidence) == 0 {
			return ""
		}
		return snap.Evidence[len(snap.Evidence)-1].Detail
	}

	detail := latestDetail()

	switch snap.Kind {
	case KindDevice:
		if snap.ID.Layer == "tunnel" {
			title = "spyder: device tunnel needs attention"
			if detail != "" {
				message = fmt.Sprintf(
					"Device %s is attached but its tunnel can't be built — try unplug/replug. (%s)",
					snap.ID.Name, detail,
				)
			} else {
				message = fmt.Sprintf(
					"Device %s is attached but its tunnel can't be built — try unplug/replug.",
					snap.ID.Name,
				)
			}
		} else {
			title = "spyder: device needs attention"
			if detail != "" {
				message = fmt.Sprintf(
					"Pinned device %s needs attention — %s",
					snap.ID.Name, detail,
				)
			} else {
				message = fmt.Sprintf(
					"Pinned device %s needs attention.",
					snap.ID.Name,
				)
			}
		}

	case KindSubprocess:
		title = "spyder: subprocess needs attention"
		if detail != "" {
			message = fmt.Sprintf(
				"spyder subprocess %s keeps failing to restart — %s",
				snap.ID.Name, detail,
			)
		} else {
			message = fmt.Sprintf(
				"spyder subprocess %s keeps failing to restart.",
				snap.ID.Name,
			)
		}

	case KindDaemon:
		title = "spyder: daemon stalled"
		if detail != "" {
			message = fmt.Sprintf(
				"spyder daemon operation is wedged-but-alive — a restart may be needed. (%s)",
				detail,
			)
		} else {
			message = "spyder daemon operation is wedged-but-alive — a restart may be needed."
		}

	default:
		// Unknown kind: fall back to something non-empty and actionable.
		title = "spyder: entity needs attention"
		if detail != "" {
			message = fmt.Sprintf("Entity %s/%s needs attention — %s", snap.Kind, snap.ID.Name, detail)
		} else {
			message = fmt.Sprintf("Entity %s/%s needs attention.", snap.Kind, snap.ID.Name)
		}
	}

	return title, message
}
