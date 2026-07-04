// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"testing"

	"github.com/marcelocantos/spyder/internal/health"
)

func deviceState(t *testing.T, m *health.Model, udid string) health.State {
	t.Helper()
	snap, ok := m.Get(deviceHealthID(udid))
	if !ok {
		return ""
	}
	return snap.State
}

// The iOS adapter's usbmux/tunnel outcomes drive per-device health through
// the classifier + model (🎯T90): a non-pinned unplug stays informational,
// a pinned unplug surfaces for attention, a re-attach heals, and repeated
// tunnel re-establish failure escalates to needs_attention.
func TestIOSAdapter_DeviceHealthReporting(t *testing.T) {
	a := NewIOSAdapter()
	m := health.New()
	a.SetHealthModel(m, func(udid string) bool { return udid == "PINNED" })

	// Non-pinned detach → absent_unexpected (informational; no alarm).
	a.reportDetach("DEV1")
	if got := deviceState(t, m, "DEV1"); got != health.AbsentUnexpected {
		t.Errorf("non-pinned detach: want AbsentUnexpected, got %q", got)
	}

	// Pinned detach → needs_attention (the developer must reconnect it).
	a.reportDetach("PINNED")
	if got := deviceState(t, m, "PINNED"); got != health.NeedsAttention {
		t.Errorf("pinned detach: want NeedsAttention, got %q", got)
	}

	// Re-attach heals the previously-absent device.
	a.reportAttach("DEV1")
	if got := deviceState(t, m, "DEV1"); got != health.Healthy {
		t.Errorf("re-attach: want Healthy, got %q", got)
	}

	// Two consecutive tunnel re-establish failures (MaxAttempts=2) escalate
	// to needs_attention — an attached device whose tunnel can't be built.
	a.reportReestablish("DEV2", errors.New("daemon did not rebuild"))
	if got := deviceState(t, m, "DEV2"); got != health.Degraded {
		t.Errorf("re-establish fail #1: want Degraded, got %q", got)
	}
	a.reportReestablish("DEV2", errors.New("daemon did not rebuild"))
	if got := deviceState(t, m, "DEV2"); got != health.NeedsAttention {
		t.Errorf("re-establish fail #2: want NeedsAttention, got %q", got)
	}

	// A later successful re-establish heals it.
	a.reportReestablish("DEV2", nil)
	if got := deviceState(t, m, "DEV2"); got != health.Healthy {
		t.Errorf("re-establish success: want Healthy, got %q", got)
	}
}

// With no health model wired (the default), the report helpers are silent
// no-ops — they must never panic on a nil model.
func TestIOSAdapter_DeviceHealthReporting_NilModelIsNoop(t *testing.T) {
	a := NewIOSAdapter()
	a.reportDetach("DEV")
	a.reportAttach("DEV")
	a.reportReestablish("DEV", errors.New("x"))
	a.reportReestablish("DEV", nil)
}
