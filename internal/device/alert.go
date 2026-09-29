// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"strings"
)

// 🎯T154 — reading and answering OS system alerts (permission prompts such
// as Local Network, notifications and App Tracking Transparency) without an
// XCTest/WebDriverAgent runner.

// SystemAlert is what an OS-owned alert on the device shows. Showing is
// false when no system alert is up; the other fields are then empty.
type SystemAlert struct {
	Showing bool     `json:"showing"`
	Owner   string   `json:"owner,omitempty"` // owning system process, e.g. SpringBoard
	Title   string   `json:"title,omitempty"`
	Message []string `json:"message,omitempty"`
	Buttons []string `json:"buttons,omitempty"`
}

// SystemAlertReader reads and answers system alerts. Platforms without it
// report the verbs as unsupported.
type SystemAlertReader interface {
	SystemAlert(id string) (SystemAlert, error)
	// TapSystemAlertButton presses the named button of the system alert
	// showing now and returns what showed before and after the tap.
	TapSystemAlertButton(id, button string) (before, after SystemAlert, err error)
}

// ErrNoSystemAlert is returned when an alert is required but none shows.
var ErrNoSystemAlert = fmt.Errorf("no system alert is showing")

// alertElement is one element in the accessibility focus cycle.
type alertElement struct {
	Description string // spoken description, e.g. "Allow, Button"
	Label       string
	PID         int
	Handle      string // platform element handle for actions
}

// buttonRole is the spoken role suffix accessibility gives buttons. It is
// English: system alerts on other languages are read but their buttons
// are not recognised.
const buttonRole = ", Button"

func (e alertElement) isButton() bool {
	return strings.HasSuffix(e.Description, buttonRole)
}

// text is what the element says, without its role suffix.
func (e alertElement) text() string {
	if e.isButton() {
		return strings.TrimSuffix(e.Description, buttonRole)
	}
	if e.Description != "" {
		return e.Description
	}
	return e.Label
}

// classifyAlert decides whether a focus cycle is a system alert: the cycle
// closed (the screen is modal) and it holds a button owned by a system
// process. The home screen is system-owned too, but its icons are not
// buttons and its focus cycle is long.
func classifyAlert(elems []alertElement, closed bool, owners map[int]string) SystemAlert {
	if !closed {
		return SystemAlert{}
	}
	var alert SystemAlert
	for _, e := range elems {
		name, system := owners[e.PID]
		if !system {
			continue // e.g. an embedded map view from another service
		}
		if e.isButton() {
			alert.Buttons = append(alert.Buttons, e.text())
			alert.Owner = name
			continue
		}
		if t := e.text(); t != "" {
			if alert.Title == "" {
				alert.Title = t
			} else {
				alert.Message = append(alert.Message, t)
			}
		}
	}
	if len(alert.Buttons) == 0 {
		return SystemAlert{}
	}
	alert.Showing = true
	return alert
}

// findButton returns the system-owned button element named label.
// Matching ignores case, surrounding space and typographic apostrophes, so
// "Don't Allow" matches iOS's "Don’t Allow".
func findButton(elems []alertElement, owners map[int]string, label string) (alertElement, bool) {
	want := normalizeLabel(label)
	for _, e := range elems {
		if _, system := owners[e.PID]; system && e.isButton() && normalizeLabel(e.text()) == want {
			return e, true
		}
	}
	return alertElement{}, false
}

// AlertHasButton reports whether the alert offers a button matching label.
func AlertHasButton(alert SystemAlert, label string) bool {
	want := normalizeLabel(label)
	for _, b := range alert.Buttons {
		if normalizeLabel(b) == want {
			return true
		}
	}
	return false
}

func normalizeLabel(s string) string {
	s = strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`).Replace(s)
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
