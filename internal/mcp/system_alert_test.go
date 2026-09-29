// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
)

// alertStub is an iOS adapter whose alert appears after a few reads.
type alertStub struct {
	*stubAdapter
	mu        sync.Mutex
	reads     int
	appearsAt int
	tapped    []string
}

var localNetwork = device.SystemAlert{Showing: true, Owner: "SpringBoard", Title: "Allow “Stock Cars” to find devices on local networks?", Buttons: []string{"Don’t Allow", "Allow"}}

func (s *alertStub) SystemAlert(string) (device.SystemAlert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.reads >= s.appearsAt && len(s.tapped) == 0 {
		return localNetwork, nil
	}
	return device.SystemAlert{}, nil
}

func (s *alertStub) TapSystemAlertButton(_ string, button string) (device.SystemAlert, device.SystemAlert, error) {
	before, _ := s.SystemAlert("")
	if !before.Showing {
		return before, device.SystemAlert{}, device.ErrNoSystemAlert
	}
	if !device.AlertHasButton(before, button) {
		return before, device.SystemAlert{}, errors.New("system alert has no button " + button)
	}
	s.mu.Lock()
	s.tapped = append(s.tapped, button)
	s.mu.Unlock()
	return before, device.SystemAlert{}, nil
}

const alertUDID = "00008103-001122334455667A"

func TestSystemAlertReadsAndTaps(t *testing.T) {
	stub := &alertStub{stubAdapter: &stubAdapter{}, appearsAt: 1}
	h := NewHandlerWithAdapters(stub, nil)
	read := dispatchJSONMap(t, h, "system_alert", map[string]any{"device": alertUDID})
	alert, _ := read["alert"].(map[string]any)
	if alert["showing"] != true || alert["owner"] != "SpringBoard" || len(alert["buttons"].([]any)) != 2 {
		t.Fatalf("system_alert: %v", read)
	}
	tap := dispatchJSONMap(t, h, "system_alert_tap", map[string]any{"device": alertUDID, "button": "Don't Allow"})
	if tap["tapped"] != "Don't Allow" || len(stub.tapped) != 1 {
		t.Fatalf("system_alert_tap: %v taps=%v", tap, stub.tapped)
	}
}

func TestSystemAlertTapWaitsForTheAlert(t *testing.T) {
	stub := &alertStub{stubAdapter: &stubAdapter{}, appearsAt: 3}
	h := NewHandlerWithAdapters(stub, nil)
	tap := dispatchJSONMap(t, h, "system_alert_tap", map[string]any{"device": alertUDID, "button": "Allow", "wait_ms": 10000.0})
	if tap["tapped"] != "Allow" || stub.reads < 3 {
		t.Fatalf("tap did not wait for the alert: %v reads=%d", tap, stub.reads)
	}
}

func TestSystemAlertTapFailsClearly(t *testing.T) {
	for _, tc := range []struct {
		name, button string
		appearsAt    int
		wait         float64
		want         string
	}{
		{"no alert", "Allow", 1000, 0, "no system alert is showing"},
		{"no alert within the wait", "Allow", 1000, 1500, "no system alert appeared within 1.5s"},
		{"no such button", "OK", 1, 0, "has no button OK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &alertStub{stubAdapter: &stubAdapter{}, appearsAt: tc.appearsAt}
			h := NewHandlerWithAdapters(stub, nil)
			r := dispatchJSON(t, h, "system_alert_tap", map[string]any{"device": alertUDID, "button": tc.button, "wait_ms": tc.wait})
			if !r.IsError || !strings.Contains(resultText(t, &r), tc.want) || len(stub.tapped) != 0 {
				t.Fatalf("got %q (error=%v), want %q", resultText(t, &r), r.IsError, tc.want)
			}
		})
	}
}

func TestSystemAlertNeedsAnAlertReader(t *testing.T) {
	h := NewHandlerWithAdapters(&stubAdapter{}, nil)
	r := dispatchJSON(t, h, "system_alert", map[string]any{"device": alertUDID})
	if !r.IsError || !strings.Contains(resultText(t, &r), "supported on iOS devices only") {
		t.Fatalf("got %q", resultText(t, &r))
	}
}
