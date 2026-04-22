// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"strings"
	"testing"
)

// Pure-function coverage of parseReadyLine (🎯T26.4). The alternative —
// spawning a subprocess that prints malformed ready lines — would be
// simulating a misbehaving bridge, which we explicitly don't do. The
// parse logic is pure and tested at its own layer.

func TestParseReadyLine_OK(t *testing.T) {
	port, token, err := parseReadyLine("ready port=12345 token=abcDEF123")
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if port != 12345 {
		t.Errorf("port = %d; want 12345", port)
	}
	if token != "abcDEF123" {
		t.Errorf("token = %q; want abcDEF123", token)
	}
}

func TestParseReadyLine_KeysReversed(t *testing.T) {
	// Order of kv pairs shouldn't matter.
	port, token, err := parseReadyLine("ready token=xyz port=9999")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if port != 9999 || token != "xyz" {
		t.Errorf("got port=%d token=%q; want 9999/xyz", port, token)
	}
}

func TestParseReadyLine_BareReady(t *testing.T) {
	_, _, err := parseReadyLine("ready")
	if err == nil {
		t.Fatal("err = nil; want malformed")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("unexpected err: %v", err)
	}
}

func TestParseReadyLine_MissingPort(t *testing.T) {
	_, _, err := parseReadyLine("ready token=xyz")
	if err == nil {
		t.Fatal("err = nil; want missing port")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("unexpected err: %v", err)
	}
}

func TestParseReadyLine_MissingToken(t *testing.T) {
	_, _, err := parseReadyLine("ready port=1234")
	if err == nil {
		t.Fatal("err = nil; want missing token")
	}
	if !strings.Contains(err.Error(), "malformed") && !strings.Contains(err.Error(), "missing") {
		t.Errorf("unexpected err: %v", err)
	}
}

func TestParseReadyLine_WrongPrefix(t *testing.T) {
	_, _, err := parseReadyLine("hello port=1 token=t")
	if err == nil {
		t.Fatal("err = nil; want malformed prefix")
	}
}

func TestParseReadyLine_InvalidPort(t *testing.T) {
	_, _, err := parseReadyLine("ready port=abc token=t")
	if err == nil {
		t.Fatal("err = nil; want invalid port")
	}
}

func TestParseReadyLine_PortOutOfRange(t *testing.T) {
	_, _, err := parseReadyLine("ready port=999999 token=t")
	if err == nil {
		t.Fatal("err = nil; want out-of-range port")
	}
}
