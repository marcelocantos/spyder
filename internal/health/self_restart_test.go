// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"testing"
	"time"
)

func TestSelfRestartLimiter_RateLimits(t *testing.T) {
	exits := 0
	l := NewSelfRestartLimiter(2, time.Hour)
	l.exitFn = func(int) { exits++ }
	now := time.Now()
	l.now = func() time.Time { return now }

	if !l.Request("wedge-1") {
		t.Fatal("first request should allow")
	}
	if !l.Request("wedge-2") {
		t.Fatal("second request should allow")
	}
	if l.Request("wedge-3") {
		t.Fatal("third request should be rate-limited")
	}
	if exits != 2 {
		t.Fatalf("exits=%d want 2", exits)
	}
}
