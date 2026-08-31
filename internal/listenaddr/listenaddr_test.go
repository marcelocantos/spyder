// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package listenaddr

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve_Precedence(t *testing.T) {
	if got := Resolve("--addr", "env", "persisted", "fallback"); got != "--addr" {
		t.Fatalf("explicit: %q", got)
	}
	if got := Resolve("", "env", "persisted", "fallback"); got != "env" {
		t.Fatalf("env: %q", got)
	}
	if got := Resolve("", "", "persisted", "fallback"); got != "persisted" {
		t.Fatalf("persisted: %q", got)
	}
	if got := Resolve("", "", "", "fallback"); got != "fallback" {
		t.Fatalf("fallback: %q", got)
	}
}

func TestIsLoopback(t *testing.T) {
	loop := []string{"127.0.0.1:3030", "localhost:3030", "[::1]:3030"}
	open := []string{":3030", "0.0.0.0:3030", "[::]:3030", "192.168.1.10:3030"}
	for _, a := range loop {
		if !IsLoopback(a) {
			t.Errorf("IsLoopback(%q) = false; want true", a)
		}
	}
	for _, a := range open {
		if IsLoopback(a) {
			t.Errorf("IsLoopback(%q) = true; want false", a)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "listen-addr")
	if Load(path) != "" {
		t.Fatal("missing file should load empty")
	}
	if err := Save(path, ":3030"); err != nil {
		t.Fatal(err)
	}
	if got := Load(path); got != ":3030" {
		t.Fatalf("Load = %q want :3030", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != ":3030\n" {
		t.Fatalf("file = %q", b)
	}
}
