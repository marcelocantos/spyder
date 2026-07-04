// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Recording fake Notifier ─────────────────────────────────────────────────

// notifyCall records one call to a fakeNotifier method.
type notifyCall struct {
	op      string // "notify" or "clear"
	key     string
	title   string
	message string
}

// fakeNotifier is a thread-safe Notifier that records every call in order.
// Tests inspect the calls slice to assert delivery behaviour without shelling
// out to terminal-notifier or osascript.
type fakeNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
}

func (f *fakeNotifier) Notify(key, title, message string) error {
	f.mu.Lock()
	f.calls = append(f.calls, notifyCall{op: "notify", key: key, title: title, message: message})
	f.mu.Unlock()
	return nil
}

func (f *fakeNotifier) Clear(key string) error {
	f.mu.Lock()
	f.calls = append(f.calls, notifyCall{op: "clear", key: key})
	f.mu.Unlock()
	return nil
}

// snapshot returns a copy of the current calls slice.
func (f *fakeNotifier) snapshot() []notifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]notifyCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// notifyCalls returns only the "notify" calls.
func (f *fakeNotifier) notifyCalls() []notifyCall {
	var out []notifyCall
	for _, c := range f.snapshot() {
		if c.op == "notify" {
			out = append(out, c)
		}
	}
	return out
}

// clearCalls returns only the "clear" calls.
func (f *fakeNotifier) clearCalls() []notifyCall {
	var out []notifyCall
	for _, c := range f.snapshot() {
		if c.op == "clear" {
			out = append(out, c)
		}
	}
	return out
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// driveToNeedsAttention registers a KindSubprocess entity with MaxAttempts=1
// and drives it to NeedsAttention via Observe(false) → RecoveryStarted →
// RecoveryFailed. Returns the ID and the detail string used in RecoveryFailed.
func driveToNeedsAttention(m *Model, name string) (ID, string) {
	id := ID{Kind: KindSubprocess, Name: name}
	const detail = "start failed: permission denied"
	m.Register(id, KindSubprocess, Policy{MaxAttempts: 1})
	m.Observe(id, false, "probe failed")
	m.RecoveryStarted(id)
	m.RecoveryFailed(id, detail)
	return id, detail
}

// ─── Test 1: FiresOnceOnNeedsAttention ───────────────────────────────────────

// TestNotifier_FiresOnceOnNeedsAttention: register subprocess Policy{MaxAttempts:1};
// drive to NeedsAttention; assert exactly ONE Notify with the right key and a
// non-empty actionable message.
func TestNotifier_FiresOnceOnNeedsAttention(t *testing.T) {
	m, _ := newTestModel()
	fn := &fakeNotifier{}
	an := NewAttentionNotifier(fn, WithNotifyClock(time.Now))
	an.Attach(m)

	id, _ := driveToNeedsAttention(m, "ios-tunnel")

	calls := fn.notifyCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 Notify call, got %d: %v", len(calls), calls)
	}
	c := calls[0]
	wantKey := notifyKey(id)
	if c.key != wantKey {
		t.Errorf("Notify key: want %q, got %q", wantKey, c.key)
	}
	if c.title == "" {
		t.Error("Notify title is empty")
	}
	if c.message == "" {
		t.Error("Notify message is empty")
	}
	// The message must name the entity and include an actionable phrase.
	if !strings.Contains(c.message, "ios-tunnel") {
		t.Errorf("message %q does not contain entity name %q", c.message, "ios-tunnel")
	}
	if !strings.Contains(c.message, "restart") {
		t.Errorf("message %q does not contain actionable phrase", c.message)
	}
}

// ─── Test 2: ClearsOnReturnToHealthy ─────────────────────────────────────────

// TestNotifier_ClearsOnReturnToHealthy: from NeedsAttention, RecoverySucceeded →
// Healthy; assert exactly one Clear for the key, and active cleared (verified by
// a subsequent NeedsAttention not being suppressed by the active flag).
func TestNotifier_ClearsOnReturnToHealthy(t *testing.T) {
	m, clk := newTestModel()
	fn := &fakeNotifier{}
	// Use a very long cooldown so the cooldown gate doesn't interfere.
	an := NewAttentionNotifier(fn,
		WithNotifyClock(clk.Now),
		WithNotifyCooldown(time.Hour),
	)
	an.Attach(m)

	id, _ := driveToNeedsAttention(m, "flakey-proc")

	// Verify the Notify fired.
	if len(fn.notifyCalls()) != 1 {
		t.Fatalf("pre-clear: want 1 Notify, got %d", len(fn.notifyCalls()))
	}

	// Recover → Healthy.
	m.RecoverySucceeded(id)

	clears := fn.clearCalls()
	if len(clears) != 1 {
		t.Fatalf("want 1 Clear call, got %d: %v", len(clears), clears)
	}
	if clears[0].key != notifyKey(id) {
		t.Errorf("Clear key: want %q, got %q", notifyKey(id), clears[0].key)
	}

	// active[id] must now be false — confirmed by checking internal state.
	an.mu.Lock()
	stillActive := an.active[id]
	an.mu.Unlock()
	if stillActive {
		t.Error("active[id] should be false after Clear")
	}
}

// ─── Test 3: SilentBelowNeedsAttention ────────────────────────────────────────

// TestNotifier_SilentBelowNeedsAttention: drive through healthy→degraded→recovering
// and MarkAbsent(unexpected); assert ZERO Notify calls.
func TestNotifier_SilentBelowNeedsAttention(t *testing.T) {
	m, _ := newTestModel()
	fn := &fakeNotifier{}
	an := NewAttentionNotifier(fn)
	an.Attach(m)

	id := ID{Kind: KindSubprocess, Name: "quiet-proc"}
	// MaxAttempts=0 means no give-up limit; entity stays Recovering indefinitely.
	m.Register(id, KindSubprocess, Policy{MaxAttempts: 0})

	// Healthy → Degraded (Observe false).
	m.Observe(id, false, "probe failed")
	// Degraded → Recovering.
	m.RecoveryStarted(id)
	// With MaxAttempts=0, RecoveryFailed stays Recovering.
	m.RecoveryFailed(id, "still failing")

	// MarkAbsent: Recovering → AbsentUnexpected (still below NeedsAttention).
	id2 := ID{Kind: KindDevice, Name: "test-device"}
	m.Register(id2, KindDevice, Policy{})
	m.MarkAbsent(id2, false, "unplugged")

	if calls := fn.notifyCalls(); len(calls) != 0 {
		t.Errorf("want 0 Notify calls below NeedsAttention, got %d: %v", len(calls), calls)
	}
}

// ─── Test 4: FlappingDoesNotSpam ─────────────────────────────────────────────

// TestNotifier_FlappingDoesNotSpam: needs_attention → Notify(1) → healthy →
// Clear → needs_attention WITHIN cooldown → assert NO second Notify. Then
// advance clock PAST cooldown → assert a second Notify fires.
func TestNotifier_FlappingDoesNotSpam(t *testing.T) {
	clk := newFakeClock()
	m := New(WithClock(clk.Now))
	fn := &fakeNotifier{}

	const testCooldown = 10 * time.Minute
	an := NewAttentionNotifier(fn,
		WithNotifyClock(clk.Now),
		WithNotifyCooldown(testCooldown),
	)
	an.Attach(m)

	id, _ := driveToNeedsAttention(m, "flapper")

	if n := len(fn.notifyCalls()); n != 1 {
		t.Fatalf("first NeedsAttention: want 1 Notify, got %d", n)
	}

	// Recover → clear.
	m.RecoverySucceeded(id)
	if n := len(fn.clearCalls()); n != 1 {
		t.Fatalf("after recovery: want 1 Clear, got %d", n)
	}

	// Advance clock LESS than cooldown.
	clk.Advance(testCooldown / 2)

	// Re-drive to NeedsAttention within cooldown.
	m.Observe(id, false, "flap")
	m.RecoveryStarted(id)
	m.RecoveryFailed(id, "flap again")

	// Still only 1 Notify (cooldown suppressed the second).
	if n := len(fn.notifyCalls()); n != 1 {
		t.Errorf("within cooldown: want 1 Notify total, got %d", n)
	}

	// Recover again so we can try past the cooldown.
	m.RecoverySucceeded(id)

	// Advance clock PAST the cooldown.
	clk.Advance(testCooldown + time.Second)

	// Re-drive to NeedsAttention past cooldown.
	m.Observe(id, false, "real fault")
	m.RecoveryStarted(id)
	m.RecoveryFailed(id, "real fault again")

	// Now a second Notify must fire.
	if n := len(fn.notifyCalls()); n != 2 {
		t.Errorf("past cooldown: want 2 Notify total, got %d", n)
	}
}

// ─── Test 5: DedupWhileActive ────────────────────────────────────────────────

// TestNotifier_DedupWhileActive: while already NeedsAttention (active),
// another transition to NeedsAttention for the SAME entity must not double-fire.
// Also asserts two independent entities each get exactly one Notify.
func TestNotifier_DedupWhileActive(t *testing.T) {
	m, _ := newTestModel()
	fn := &fakeNotifier{}
	an := NewAttentionNotifier(fn)
	an.Attach(m)

	// Drive entity A to NeedsAttention.
	idA := ID{Kind: KindSubprocess, Name: "proc-a"}
	m.Register(idA, KindSubprocess, Policy{MaxAttempts: 1})
	m.Observe(idA, false, "a-fail")
	m.RecoveryStarted(idA)
	m.RecoveryFailed(idA, "a still failing")

	if n := len(fn.notifyCalls()); n != 1 {
		t.Fatalf("proc-a: want 1 Notify, got %d", n)
	}

	// Drive entity B (independent) to NeedsAttention.
	idB := ID{Kind: KindSubprocess, Name: "proc-b"}
	m.Register(idB, KindSubprocess, Policy{MaxAttempts: 1})
	m.Observe(idB, false, "b-fail")
	m.RecoveryStarted(idB)
	m.RecoveryFailed(idB, "b still failing")

	if n := len(fn.notifyCalls()); n != 2 {
		t.Fatalf("proc-a+b: want 2 Notify total, got %d", n)
	}

	// Manually trigger another transition to NeedsAttention for A while it is
	// still active. We do this by registering a direct observer that fires
	// a synthetic transition; simpler: just re-call RecoveryFailed (which
	// stays NeedsAttention → NeedsAttention self-transition isn't emitted by
	// the model, so simulate via direct notify call). Use the AttentionNotifier
	// directly to confirm the active dedup path.
	an.onTransition(Transition{ID: idA, From: NeedsAttention, To: NeedsAttention})

	// Still only 2 Notify calls — the dedup prevented a third.
	if n := len(fn.notifyCalls()); n != 2 {
		t.Errorf("dedup: want 2 Notify total after re-entry, got %d", n)
	}
}

// ─── Test 6: BuildMessage_PerKind ────────────────────────────────────────────

// TestBuildMessage_PerKind: table over every Kind/Layer combination, asserting
// non-empty, actionable, entity-naming title+message. Also verifies that
// empty Evidence does not panic.
func TestBuildMessage_PerKind(t *testing.T) {
	evidence := []Observation{{At: time.Now(), OK: false, Detail: "detailed reason"}}

	cases := []struct {
		name          string
		snap          EntitySnapshot
		wantTitleSub  string
		wantMsgSub    string   // must appear in message
		wantMsgEntity []string // entity-identifying strings that must appear
	}{
		{
			name: "KindDevice/tunnel",
			snap: EntitySnapshot{
				ID:       ID{Kind: KindDevice, Name: "iPad-Pro", Layer: "tunnel"},
				Kind:     KindDevice,
				Evidence: evidence,
			},
			wantTitleSub:  "tunnel",
			wantMsgSub:    "unplug",
			wantMsgEntity: []string{"iPad-Pro", "detailed reason"},
		},
		{
			name: "KindDevice/pinned (no layer)",
			snap: EntitySnapshot{
				ID:       ID{Kind: KindDevice, Name: "iPhone-15"},
				Kind:     KindDevice,
				Evidence: evidence,
			},
			wantTitleSub:  "device",
			wantMsgSub:    "attention",
			wantMsgEntity: []string{"iPhone-15", "detailed reason"},
		},
		{
			name: "KindSubprocess",
			snap: EntitySnapshot{
				ID:       ID{Kind: KindSubprocess, Name: "ios-tunnel"},
				Kind:     KindSubprocess,
				Evidence: evidence,
			},
			wantTitleSub:  "subprocess",
			wantMsgSub:    "restart",
			wantMsgEntity: []string{"ios-tunnel", "detailed reason"},
		},
		{
			name: "KindDaemon",
			snap: EntitySnapshot{
				ID:       ID{Kind: KindDaemon, Name: "spyder"},
				Kind:     KindDaemon,
				Evidence: evidence,
			},
			wantTitleSub:  "daemon",
			wantMsgSub:    "restart",
			wantMsgEntity: []string{"detailed reason"},
		},
		{
			name: "KindDevice/tunnel/no evidence (no panic)",
			snap: EntitySnapshot{
				ID:       ID{Kind: KindDevice, Name: "empty-device", Layer: "tunnel"},
				Kind:     KindDevice,
				Evidence: nil, // must not panic
			},
			wantTitleSub:  "tunnel",
			wantMsgSub:    "unplug",
			wantMsgEntity: []string{"empty-device"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Must not panic even with empty evidence.
			title, message := buildMessage(tc.snap)

			if title == "" {
				t.Error("title is empty")
			}
			if message == "" {
				t.Error("message is empty")
			}
			if !strings.Contains(title, tc.wantTitleSub) {
				t.Errorf("title %q does not contain %q", title, tc.wantTitleSub)
			}
			if !strings.Contains(message, tc.wantMsgSub) {
				t.Errorf("message %q does not contain %q", message, tc.wantMsgSub)
			}
			for _, sub := range tc.wantMsgEntity {
				if !strings.Contains(title+message, sub) {
					t.Errorf("title+message does not contain entity string %q", sub)
				}
			}
		})
	}
}

// ─── Test: notifyKey format ───────────────────────────────────────────────────

func TestNotifyKey(t *testing.T) {
	cases := []struct {
		id   ID
		want string
	}{
		{ID{Kind: KindDevice, Name: "iPad", Layer: "tunnel"}, "device/iPad/tunnel"},
		{ID{Kind: KindSubprocess, Name: "ios-tunnel"}, "subprocess/ios-tunnel"},
		{ID{Kind: KindDaemon, Name: "spyder"}, "daemon/spyder"},
	}
	for _, tc := range cases {
		got := notifyKey(tc.id)
		if got != tc.want {
			t.Errorf("notifyKey(%v) = %q; want %q", tc.id, got, tc.want)
		}
	}
}

// ─── Test: sanitizeForAppleScript ────────────────────────────────────────────

func TestSanitizeForAppleScript(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`hello`, `hello`},
		{`say "hi"`, `say \"hi\"`},
		{`back\slash`, `back\\slash`},
		{`"quote" and \slash`, `\"quote\" and \\slash`},
	}
	for _, tc := range cases {
		got := sanitizeForAppleScript(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeForAppleScript(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

// ─── Test: fakeNotifier thread safety ────────────────────────────────────────

// TestFakeNotifier_ThreadSafety hammers the fakeNotifier from multiple goroutines
// to confirm it is race-clean under -race.
func TestFakeNotifier_ThreadSafety(t *testing.T) {
	fn := &fakeNotifier{}
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i)
			_ = fn.Notify(key, "title", "msg")
			_ = fn.Clear(key)
		}(i)
	}
	wg.Wait()
	// 20 goroutines × (1 Notify + 1 Clear) = 40 calls.
	if n := len(fn.snapshot()); n != 40 {
		t.Errorf("want 40 calls, got %d", n)
	}
}
