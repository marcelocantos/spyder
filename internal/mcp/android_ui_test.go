// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T180 — input_text / input_key / ui_tree / ui_tap / screen_secure.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
)

type uiStub struct {
	*stubAdapter
	typed    []string
	modes    []string
	tree     device.UITree
	treeErr  error
	taps     [][2]int
	keys     [][]int
	secure   device.ScreenSecureInfo
	inputErr error
}

func (s *uiStub) InputTextContext(_ context.Context, _ string, text, mode string) (device.TextInputResult, error) {
	s.typed = append(s.typed, text)
	s.modes = append(s.modes, mode)
	if s.inputErr != nil {
		return device.TextInputResult{}, s.inputErr
	}
	return device.TextInputResult{Mode: "text", Steps: 1, Chars: len(text)}, nil
}

func (s *uiStub) UITreeContext(context.Context, string) (device.UITree, error) {
	return s.tree, s.treeErr
}

func (s *uiStub) ScreenSecureContext(context.Context, string, bool) (device.ScreenSecureInfo, error) {
	return s.secure, nil
}

func (s *uiStub) InjectTapContext(_ context.Context, _ string, x, y int) error {
	s.taps = append(s.taps, [2]int{x, y})
	return nil
}

func (s *uiStub) InjectSwipeContext(context.Context, string, int, int, int, int, int) error {
	return nil
}

func (s *uiStub) InjectKeyEventContext(_ context.Context, _ string, c int) error {
	s.keys = append(s.keys, []int{c})
	return nil
}

func (s *uiStub) InjectKeyEventsContext(_ context.Context, _ string, cs []int) error {
	s.keys = append(s.keys, cs)
	return nil
}

func newUIStub() *uiStub {
	n := func(text, rid, desc string, b [4]int, enabled bool) *device.UINode {
		return &device.UINode{Class: "android.widget.TextView", Text: text, ResourceID: rid, ContentDesc: desc,
			Bounds: b, Center: [2]int{(b[0] + b[2]) / 2, (b[1] + b[3]) / 2}, Enabled: enabled, Clickable: true}
	}
	root := &device.UINode{Class: "android.widget.FrameLayout", Bounds: [4]int{0, 0, 1000, 2000}, Enabled: true}
	root.Children = []*device.UINode{
		n("Search settings", "com.android.settings:id/search", "", [4]int{0, 100, 1000, 200}, true),
		n("Item", "", "", [4]int{0, 300, 1000, 400}, true),
		n("Item", "", "", [4]int{0, 500, 1000, 600}, true),
		n("Greyed", "", "", [4]int{0, 700, 1000, 800}, false),
		n("", "", "Navigate up", [4]int{0, 0, 100, 100}, true),
	}
	return &uiStub{stubAdapter: &stubAdapter{}, tree: device.UITree{Package: "com.android.settings", Nodes: []*device.UINode{root}}}
}

func TestInputText_NeverEchoesText(t *testing.T) {
	s := newUIStub()
	h := newHandlerWithStubs(t, nil, s)
	for _, tc := range []struct {
		text     string
		secret   any
		redacted bool
	}{
		{"hello world", nil, false},
		{"hunter2", true, true},
		{"4071", nil, true}, // PIN-like → auto-redacted
	} {
		args := map[string]any{"device": "Raspberry", "text": tc.text}
		if tc.secret != nil {
			args["secret"] = tc.secret
		}
		r := dispatchJSON(t, h, "input_text", args)
		body := resultText(t, &r)
		if r.IsError {
			t.Fatalf("%q: %s", tc.text, body)
		}
		if strings.Contains(body, tc.text) {
			t.Fatalf("response echoes text: %s", body)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		if m["redacted"] != tc.redacted || m["chars"].(float64) != float64(len(tc.text)) {
			t.Fatalf("%q: body=%s", tc.text, body)
		}
	}
	if len(s.typed) != 3 || s.typed[1] != "hunter2" {
		t.Fatalf("typed=%q", s.typed)
	}
}

func TestInputText_ErrorScrubbed(t *testing.T) {
	s := newUIStub()
	s.inputErr = errors.New("adb failed typing s3cr3t pass and s3cr3t%spass")
	h := newHandlerWithStubs(t, nil, s)
	r := dispatchJSON(t, h, "input_text", map[string]any{"device": "Raspberry", "text": "s3cr3t pass", "secret": true})
	body := resultText(t, &r)
	if !r.IsError || strings.Contains(body, "s3cr3t") {
		t.Fatalf("want scrubbed error, got %s", body)
	}
}

func TestInputText_IOSFailsClosed(t *testing.T) {
	h := newHandlerWithStubs(t, newUIStub(), nil)
	for _, verb := range []string{"input_text", "ui_tree", "ui_tap", "screen_secure"} {
		r := dispatchJSON(t, h, verb, map[string]any{"device": "iPad", "text": "x"})
		body := resultText(t, &r)
		if !r.IsError || !strings.Contains(body, "not supported on iOS") {
			t.Errorf("%s on iOS: %s", verb, body)
		}
	}
}

func TestInputText_ReservationGated(t *testing.T) {
	s := newUIStub()
	h, _, _ := newHandlerWithRuns(t, nil, s)
	h.noProvenanceProbe = true
	if r := dispatchJSON(t, h, "reserve", map[string]any{"device": "Raspberry", "owner": "alice"}); r.IsError {
		t.Fatalf("reserve: %s", resultText(t, &r))
	}
	for _, verb := range []string{"input_text", "ui_tap"} {
		r := dispatchJSON(t, h, verb, map[string]any{"device": "Raspberry", "text": "Item", "owner": "bob"})
		if !r.IsError {
			t.Errorf("%s by bob on alice's device should fail: %s", verb, resultText(t, &r))
		}
	}
	// Observational verbs stay ungated.
	for _, verb := range []string{"ui_tree", "screen_secure"} {
		if r := dispatchJSON(t, h, verb, map[string]any{"device": "Raspberry", "owner": "bob"}); r.IsError {
			t.Errorf("%s should be ungated: %s", verb, resultText(t, &r))
		}
	}
	if r := dispatchJSON(t, h, "input_text", map[string]any{"device": "Raspberry", "text": "ok", "owner": "alice"}); r.IsError {
		t.Fatalf("owner should pass: %s", resultText(t, &r))
	}
}

func TestUITree_CompactAndFull(t *testing.T) {
	h := newHandlerWithStubs(t, nil, newUIStub())
	full := dispatchJSONMap(t, h, "ui_tree", map[string]any{"device": "Raspberry"})
	if full["node_count"].(float64) != 6 || full["package"] != "com.android.settings" {
		t.Fatalf("full=%v", full)
	}
	c := dispatchJSONMap(t, h, "ui_tree", map[string]any{"device": "Raspberry", "compact": true})
	if c["node_count"].(float64) != 5 || c["raw_count"].(float64) != 6 {
		t.Fatalf("compact=%v", c)
	}
}

func TestUITap(t *testing.T) {
	s := newUIStub()
	h := newHandlerWithStubs(t, nil, s)
	r := dispatchJSONMap(t, h, "ui_tap", map[string]any{"device": "Raspberry", "text": "Search settings"})
	if r["x"].(float64) != 500 || r["y"].(float64) != 150 || len(s.taps) != 1 {
		t.Fatalf("tap=%v taps=%v", r, s.taps)
	}
	dispatchJSONMap(t, h, "ui_tap", map[string]any{"device": "Raspberry", "resource_id": "search"})
	dispatchJSONMap(t, h, "ui_tap", map[string]any{"device": "Raspberry", "desc": "navigate", "contains": true})
	if len(s.taps) != 3 || s.taps[2] != [2]int{50, 50} {
		t.Fatalf("taps=%v", s.taps)
	}
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"text": "Nope"}, "no on-screen node"},
		{map[string]any{"text": "Item"}, "2 nodes match"},
		{map[string]any{"text": "Item", "index": 5.0}, "out of range"},
		{map[string]any{"text": "Greyed"}, "disabled"},
		{map[string]any{}, "one of text, resource_id, or desc"},
	} {
		tc.args["device"] = "Raspberry"
		res := dispatchJSON(t, h, "ui_tap", tc.args)
		if body := resultText(t, &res); !res.IsError || !strings.Contains(body, tc.want) {
			t.Errorf("%v: want %q, got %s", tc.args, tc.want, body)
		}
	}
	if len(s.taps) != 3 {
		t.Fatalf("failed selectors must not tap: %v", s.taps)
	}
	r = dispatchJSONMap(t, h, "ui_tap", map[string]any{"device": "Raspberry", "text": "Item", "index": 1.0})
	if r["y"].(float64) != 550 {
		t.Fatalf("index pick: %v", r)
	}
}

func TestInputKey_Extended(t *testing.T) {
	s := newUIStub()
	h := newHandlerWithStubs(t, nil, s)
	dispatchJSONMap(t, h, "input_key", map[string]any{"device": "Raspberry", "key": "enter"})
	dispatchJSONMap(t, h, "input_key", map[string]any{"device": "Raspberry", "key": "KEYCODE_TAB"})
	dispatchJSONMap(t, h, "input_key", map[string]any{"device": "Raspberry", "key": "67", "repeat": 3.0})
	if len(s.keys) != 3 || s.keys[0][0] != 66 || s.keys[1][0] != 61 || len(s.keys[2]) != 3 || s.keys[2][0] != 67 {
		t.Fatalf("keys=%v", s.keys)
	}
	r := dispatchJSON(t, h, "input_key", map[string]any{"device": "Raspberry", "key": "frobnicate"})
	if body := resultText(t, &r); !r.IsError || !strings.Contains(body, "enter") {
		t.Fatalf("unknown key: %s", body)
	}
}

func TestScreenSecure(t *testing.T) {
	s := newUIStub()
	s.secure = device.ScreenSecureInfo{WindowSecureInfo: device.WindowSecureInfo{
		FocusedWindow: "com.bank/.Main", Package: "com.bank", FocusedSecure: true,
		SecureWindows: []device.SecureWindow{{Hash: "1", Name: "com.bank/.Main"}},
	}, Verdict: "secure"}
	h := newHandlerWithStubs(t, nil, s)
	r := dispatchJSONMap(t, h, "screen_secure", map[string]any{"device": "Raspberry"})
	if r["verdict"] != "secure" || r["secure_flag"] != true || r["package"] != "com.bank" {
		t.Fatalf("r=%v", r)
	}
}
