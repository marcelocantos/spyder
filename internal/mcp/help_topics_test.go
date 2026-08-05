// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"
)

// 🎯T113: help("app") returns a focused topical guide with copy-paste
// recipes, not the full verb dump.
func TestExec_HelpTopicApp(t *testing.T) {
	res := runScript(t, `help("app")`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	got := texts(res)[0]
	for _, want := range []string{"app_channel_list", "app_input", "app_screenshot", "recipes:"} {
		if !strings.Contains(got, want) {
			t.Errorf("help(\"app\") missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "say_text") {
		t.Errorf("help(\"app\") should be topical, not the verb dump:\n%s", got)
	}
}

// Every registered topic resolves, and each guide carries recipes.
func TestExec_HelpAllTopicsResolve(t *testing.T) {
	for _, topic := range helpTopicNames() {
		res := runScript(t, `help("`+topic+`")`, stubVerbs(), defaultLim())
		if res.IsError {
			t.Fatalf("help(%q): %v", topic, texts(res))
		}
		if got := texts(res)[0]; !strings.Contains(got, "recipes:") {
			t.Errorf("help(%q) has no recipes section:\n%s", topic, got)
		}
	}
}

// An unknown topic errors and names the valid topics.
func TestExec_HelpUnknownTopicListsTopics(t *testing.T) {
	res := runScript(t, `help("nonsense")`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("help of unknown topic should error")
	}
	got := strings.Join(texts(res), "\n")
	if !strings.Contains(got, "unknown topic") {
		t.Errorf("error should say unknown topic: %s", got)
	}
	for _, topic := range helpTopicNames() {
		if !strings.Contains(got, topic) {
			t.Errorf("error should list topic %q: %s", topic, got)
		}
	}
}

// Bare help() keeps the full verb dump and advertises the topics.
func TestExec_HelpBareListsTopics(t *testing.T) {
	res := runScript(t, `help()`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	got := texts(res)[0]
	if !strings.Contains(got, "say_text") {
		t.Errorf("bare help() must still list verbs: %s", got)
	}
	if !strings.Contains(got, "topics:") {
		t.Errorf("bare help() must advertise topics: %s", got)
	}
	for _, topic := range helpTopicNames() {
		if !strings.Contains(got, topic) {
			t.Errorf("bare help() missing topic %q: %s", topic, got)
		}
	}
}

// 🎯T116: the reservations topic states the gating policy.
func TestExec_HelpReservationsTopicStatesPolicy(t *testing.T) {
	res := runScript(t, `help("reservations")`, stubVerbs(), defaultLim())
	if res.IsError {
		t.Fatalf("unexpected error: %v", texts(res))
	}
	got := texts(res)[0]
	for _, want := range []string{"device-state-mutating", "always succeed", "reservation_status"} {
		if !strings.Contains(got, want) {
			t.Errorf("help(\"reservations\") missing %q:\n%s", want, got)
		}
	}
}
