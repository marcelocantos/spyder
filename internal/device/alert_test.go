// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"encoding/base64"
	"slices"
	"testing"
)

// The focus cycle Jevons (iPad mini, iOS 27) produced for Stock Cars'
// Local Network alert on 2026-09-29. SpringBoard is pid 38; the embedded
// map belongs to another process (pid 3179).
var localNetworkCycle = []alertElement{
	{Description: "Allow “Stock Cars” to find devices on local networks?", PID: 38},
	{Description: "Development builds connect to the spyder automation host on your local network for testing.", PID: 38},
	{Description: "Map, Image", PID: 3179},
	{Description: "Map pin, Shows more info", PID: 3179},
	{Description: "Information from networks can be used to determine your location and create a profile of you.", PID: 3179},
	{Description: "Don’t Allow, Button", Label: "Don’t Allow", PID: 38, Handle: "dont"},
	{Description: "Allow, Button", Label: "Allow", PID: 38, Handle: "allow"},
}

var springboard = map[int]string{38: "SpringBoard"}

func TestClassifyAlertReadsASpringBoardPrompt(t *testing.T) {
	alert := classifyAlert(localNetworkCycle, true, springboard)
	if !alert.Showing || alert.Owner != "SpringBoard" || alert.Title != "Allow “Stock Cars” to find devices on local networks?" {
		t.Fatalf("alert: %+v", alert)
	}
	if !slices.Equal(alert.Buttons, []string{"Don’t Allow", "Allow"}) || len(alert.Message) != 1 {
		t.Fatalf("buttons/message: %+v", alert)
	}
}

func TestClassifyAlertRejectsNonAlerts(t *testing.T) {
	home := []alertElement{
		{Description: "TiltBuggy, Updates Frequently", PID: 38},
		{Description: "Photos, Updates Frequently", PID: 38},
		{Description: "Page 3 of 3, Adjustable", PID: 38},
	}
	appButtons := []alertElement{
		{Description: "Play, Button", PID: 3223},
		{Description: "Settings, Button", PID: 3223},
	}
	for name, tc := range map[string]struct {
		elems  []alertElement
		closed bool
	}{
		"home screen (no buttons)":           {home, true},
		"app's own buttons":                  {appButtons, true},
		"focus did not close (long screen)":  {localNetworkCycle, false},
		"game view (no accessible elements)": {nil, false},
	} {
		if alert := classifyAlert(tc.elems, tc.closed, springboard); alert.Showing {
			t.Errorf("%s: classified as a system alert: %+v", name, alert)
		}
	}
}

func TestFindButtonMatchesLooselyButOnlySystemButtons(t *testing.T) {
	for _, label := range []string{"Don't Allow", "don’t allow", "  Don't   Allow "} {
		if e, ok := findButton(localNetworkCycle, springboard, label); !ok || e.Handle != "dont" {
			t.Errorf("%q: %+v %v", label, e, ok)
		}
	}
	if _, ok := findButton(localNetworkCycle, springboard, "Ask App Not to Track"); ok {
		t.Error("found a button the alert does not have")
	}
	if _, ok := findButton([]alertElement{{Description: "Allow, Button", PID: 3223}}, springboard, "Allow"); ok {
		t.Error("matched an app-owned button")
	}
	alert := classifyAlert(localNetworkCycle, true, springboard)
	if !AlertHasButton(alert, "don't allow") || AlertHasButton(alert, "OK") {
		t.Errorf("AlertHasButton: %+v", alert.Buttons)
	}
}

func TestElementPIDReadsTheHandlePrefix(t *testing.T) {
	// Handles from the same session: SpringBoard (0x26) and the map (0x0c6b).
	for hexHandle, want := range map[string]int{
		"JgAAAABVfyB5AAAAIQAAAAAAAAA=": 38,
		"awwAAADA2aR7AAAABAAAAAAAAAA=": 3179,
	} {
		if got := elementPID(hexHandle); got != want {
			raw, _ := base64.StdEncoding.DecodeString(hexHandle)
			t.Errorf("%x: pid %d, want %d", raw, got, want)
		}
	}
	if elementPID("!!") != -1 || elementPID("") != -1 {
		t.Error("bad handles must not yield a pid")
	}
}
