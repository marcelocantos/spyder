// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devpolicy

import (
	"log/slog"
	"time"
)

// PushLog records one dev-policy configure attempt for attribution in daemon logs.
type PushLog struct {
	SessionID string
	DeviceID  string
	BundleID  string
	Flags     Flags
	Lease     time.Duration
	Latency   time.Duration
	Outcome   string // renew, revoke, or error
	Err       string
}

// logPush emits a structured dev-policy push record. Tests may replace LogPush on
// the controller; production uses slog by default.
func (c *Controller) logPush(rec PushLog) {
	if c.LogPush != nil {
		c.LogPush(rec)
		return
	}
	attrs := []any{
		"session_id", rec.SessionID,
		"device_id", rec.DeviceID,
		"bundle_id", rec.BundleID,
		"outcome", rec.Outcome,
		"latency_ms", rec.Latency.Milliseconds(),
		"lease_ms", rec.Lease.Milliseconds(),
	}
	if len(rec.Flags) > 0 {
		attrs = append(attrs, "flags", rec.Flags)
	}
	if rec.Err != "" {
		attrs = append(attrs, "error", rec.Err)
	}
	slog.Info("dev-policy: push", attrs...)
}
