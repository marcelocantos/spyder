// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The exact error a device without its developer disk image returns.
const ddiResetErr = "new: ConnectToXpcServiceTunnelIface: failed to connect to http2: NewHttpConnection: could not read frame. read tcp 127.0.0.1:49807->127.0.0.1:60108: read: connection reset by peer"

func TestIsDDIUnavailable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New(ddiResetErr), true},
		{errors.New("ConnectToXpcServiceTunnelIface: EOF"), true},
		{errors.New("goios: stale or missing device tunnel"), false},
		{errors.New("read: connection reset by peer"), false}, // not the app-service connect
		{nil, false},
	}
	for _, c := range cases {
		if got := isDDIUnavailable(c.err); got != c.want {
			t.Errorf("isDDIUnavailable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestDDIHealRateLimited(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	runs := 0
	fail := false
	h := &ddiHealer{last: map[string]time.Time{}, now: func() time.Time { return now },
		run: func(context.Context, string) error {
			runs++
			if fail {
				return errors.New("devicectl: device locked")
			}
			return nil
		}}
	if !h.heal("A") || runs != 1 {
		t.Fatalf("first heal: runs=%d", runs)
	}
	if h.heal("A") || runs != 1 {
		t.Fatalf("heal within the window ran again: runs=%d", runs)
	}
	if !h.heal("B") || runs != 2 {
		t.Fatalf("other device not healed: runs=%d", runs)
	}
	now = now.Add(ddiHealEvery)
	fail = true
	if h.heal("A") || runs != 3 {
		t.Fatalf("failed heal reported success or did not run: runs=%d", runs)
	}
	if h.heal("") {
		t.Fatal("empty udid healed")
	}
}
