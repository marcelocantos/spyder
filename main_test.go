// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/daemon"
	"github.com/marcelocantos/spyder/internal/listenaddr"
	"github.com/marcelocantos/spyder/internal/listenpreflight"
	"github.com/marcelocantos/spyder/internal/paths"
	"strings"
)

func TestParseServeAddr(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		args                 []string
		env, persisted, want string
		explicit             bool
	}{
		{"default", nil, "", "", defaultAddr, false},
		{"persisted", nil, "", ":3030", ":3030", false},
		// 🎯T151: SPYDER_ADDR is a caller override too — serving a tree
		// build on another port via env must not look like a plain
		// restart that should re-persist the resolved bind.
		{"env", nil, ":4040", ":3030", ":4040", true},
		{"flag", []string{"--addr", ":5050"}, ":4040", ":3030", ":5050", true},
		{"short flag", []string{"-a", ":5050"}, "", ":3030", ":5050", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, explicit, err := parseServeAddr(tc.args, tc.env, tc.persisted)
			if err != nil || got != tc.want || explicit != tc.explicit {
				t.Fatalf("got %q explicit=%v err=%v; want %q explicit=%v", got, explicit, err, tc.want, tc.explicit)
			}
		})
	}
	if _, _, err := parseServeAddr([]string{"--addr"}, "", ""); err == nil {
		t.Fatal("missing --addr value should error")
	}
}

// Regression for 🎯T151: a test-port --addr must not steal the
// supervisor's persisted address. Run only on a local ephemeral port and
// redirect HOME, never touch the actual daemon or ~/.spyder.
func TestServeExplicitAddrLeavesRememberedAddr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := paths.ListenAddrPath()
	if err := listenaddr.Save(path, ":3030"); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().String()
	ln.Close()
	addr, explicit, err := parseServeAddr([]string{"--addr", port}, "", listenaddr.Load(path))
	if err != nil || !explicit || addr != port {
		t.Fatalf("explicit resolution: addr=%q explicit=%v err=%v", addr, explicit, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- daemon.Run(ctx, daemon.Config{Addr: addr, ExplicitAddr: explicit, Version: "test"}) }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", port, 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			break
		}
		select {
		case runErr := <-errCh:
			t.Fatalf("test server stopped before listening: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("test server did not listen at %s: %v", port, dialErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("test server: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test server did not stop")
	}
	if got := listenaddr.Load(path); got != ":3030" {
		t.Fatalf("--addr replaced supervisor remembered address with %q", got)
	}
	got, override, err := parseServeAddr(nil, "", listenaddr.Load(path))
	if err != nil || override || got != ":3030" {
		t.Fatalf("no-flag restart: addr=%q explicit=%v err=%v; want :3030", got, override, err)
	}
}

// Regression for 🎯T151: SPYDER_ADDR (used to serve a tree build on
// another port alongside the supervised daemon) must not steal the
// supervisor's persisted address either — only --addr was covered by
// TestServeExplicitAddrLeavesRememberedAddr above. Run only on a local
// ephemeral port and redirect HOME, never touch the actual daemon or
// ~/.spyder.
func TestServeEnvAddrLeavesRememberedAddr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := paths.ListenAddrPath()
	if err := listenaddr.Save(path, ":3030"); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().String()
	ln.Close()
	addr, explicit, err := parseServeAddr(nil, port, listenaddr.Load(path))
	if err != nil || !explicit || addr != port {
		t.Fatalf("env resolution: addr=%q explicit=%v err=%v", addr, explicit, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- daemon.Run(ctx, daemon.Config{Addr: addr, ExplicitAddr: explicit, Version: "test"}) }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", port, 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			break
		}
		select {
		case runErr := <-errCh:
			t.Fatalf("test server stopped before listening: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("test server did not listen at %s: %v", port, dialErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("test server: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test server did not stop")
	}
	if got := listenaddr.Load(path); got != ":3030" {
		t.Fatalf("SPYDER_ADDR replaced supervisor remembered address with %q", got)
	}
}

func TestParseRunArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantDev string
		wantCmd []string
		wantErr string // substring match; empty means no error expected
	}{
		{
			name:    "empty",
			args:    nil,
			wantErr: "no command provided",
		},
		{
			name:    "only separator",
			args:    []string{"--"},
			wantErr: "no command provided",
		},
		{
			name:    "just command",
			args:    []string{"echo"},
			wantDev: defaultRunDevice,
			wantCmd: []string{"echo"},
		},
		{
			name:    "command with args",
			args:    []string{"echo", "hello", "world"},
			wantDev: defaultRunDevice,
			wantCmd: []string{"echo", "hello", "world"},
		},
		{
			name:    "separator then command",
			args:    []string{"--", "echo", "hello"},
			wantDev: defaultRunDevice,
			wantCmd: []string{"echo", "hello"},
		},
		{
			name:    "long device flag",
			args:    []string{"--device", "Foo", "--", "echo"},
			wantDev: "Foo",
			wantCmd: []string{"echo"},
		},
		{
			name:    "short device flag",
			args:    []string{"-d", "Foo", "echo"},
			wantDev: "Foo",
			wantCmd: []string{"echo"},
		},
		{
			name:    "device flag without separator",
			args:    []string{"--device", "Foo", "echo", "arg"},
			wantDev: "Foo",
			wantCmd: []string{"echo", "arg"},
		},
		{
			name:    "device flag missing value",
			args:    []string{"--device"},
			wantErr: "--device requires a value",
		},
		{
			name:    "unknown flag",
			args:    []string{"--bogus"},
			wantErr: `unknown flag "--bogus"`,
		},
		{
			name: "command that happens to start with dash after separator",
			// With `--`, everything after is a positional command even if it
			// starts with a dash (e.g. test wrappers that take --flag args).
			args:    []string{"--", "myscript", "--flag", "value"},
			wantDev: defaultRunDevice,
			wantCmd: []string{"myscript", "--flag", "value"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseRunArgs(c.args)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("parseRunArgs(%v) err = nil; want error containing %q",
						c.args, c.wantErr)
				}
				if !containsStr(err.Error(), c.wantErr) {
					t.Fatalf("parseRunArgs(%v) err = %q; want error containing %q",
						c.args, err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRunArgs(%v) err = %v", c.args, err)
			}
			if got.Device != c.wantDev {
				t.Errorf("dev = %q; want %q", got.Device, c.wantDev)
			}
			if !reflect.DeepEqual(got.Command, c.wantCmd) {
				t.Errorf("cmd = %v; want %v", got.Command, c.wantCmd)
			}
		})
	}
}

func TestParseRunArgs_Timeout(t *testing.T) {
	got, err := parseRunArgs([]string{"--timeout", "90s", "--", "echo"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Timeout != 90*time.Second {
		t.Errorf("Timeout = %v; want 90s", got.Timeout)
	}
	if got.Device != defaultRunDevice {
		t.Errorf("Device = %q; want %q", got.Device, defaultRunDevice)
	}
}

func TestParseRunArgs_TimeoutInvalid(t *testing.T) {
	_, err := parseRunArgs([]string{"--timeout", "banana", "--", "echo"})
	if err == nil || !containsStr(err.Error(), "--timeout") {
		t.Fatalf("err = %v; want --timeout parse error", err)
	}
}

func TestParseRunArgs_TimeoutNonPositive(t *testing.T) {
	_, err := parseRunArgs([]string{"--timeout", "0s", "--", "echo"})
	if err == nil || !containsStr(err.Error(), "--timeout must be positive") {
		t.Fatalf("err = %v; want positive-timeout error", err)
	}
}

func TestParseRunArgs_OnPredicate(t *testing.T) {
	got, err := parseRunArgs([]string{"--on", "platform=ios,os>=17", "--", "echo"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.On != "platform=ios,os>=17" {
		t.Errorf("On = %q; want %q", got.On, "platform=ios,os>=17")
	}
	// --on suppresses defaultRunDevice — it'll be filled in after daemon resolution.
	if got.Device != "" {
		t.Errorf("Device = %q; want empty (resolved from --on at run time)", got.Device)
	}
}

func TestParseRunArgs_OnAndDeviceMutuallyExclusive(t *testing.T) {
	_, err := parseRunArgs([]string{"--device", "Foo", "--on", "platform=ios", "--", "echo"})
	if err == nil || !containsStr(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v; want mutual-exclusion error", err)
	}
}

func TestParseRunArgs_AsFlag(t *testing.T) {
	got, err := parseRunArgs([]string{"--as", "myproject", "--", "echo", "hi"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Owner != "myproject" {
		t.Errorf("Owner = %q; want myproject", got.Owner)
	}
}

func TestDeriveOwner_FallbackToCwd(t *testing.T) {
	// Supplied non-empty wins.
	if got := deriveOwner("explicit"); got != "explicit" {
		t.Errorf("deriveOwner('explicit') = %q; want explicit", got)
	}
	// Empty → derived from cwd basename.
	// Just check it's non-empty; the actual basename depends on the
	// test invocation directory.
	if got := deriveOwner(""); got == "" {
		t.Error("deriveOwner('') returned empty; want cwd basename")
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || indexOfStr(s, sub) >= 0
}

// indexOfStr — avoids importing strings just for the one Contains call
// (main_test.go should stay lean).
func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 🎯T166: spyder run carries run-provenance flags into runArgs.
func TestParseRunArgs_Provenance(t *testing.T) {
	got, err := parseRunArgs([]string{
		"--device", "A23", "--script", "euan_shop_smoke", "--app-bundle", "com.example.scr",
		"--app-name", "Stock Car Racing", "--app-commit", "c0ffee", "--app-branch", "main",
		"--app-dirty", "true", "--", "echo", "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Script != "euan_shop_smoke" || got.AppBundle != "com.example.scr" || got.AppName != "Stock Car Racing" ||
		got.AppCommit != "c0ffee" || got.AppBranch != "main" || got.AppDirty != "true" || got.Device != "A23" {
		t.Errorf("got %+v", got)
	}
	if _, err := parseRunArgs([]string{"--app-dirty", "maybe", "--", "x"}); err == nil {
		t.Error("--app-dirty maybe should be rejected")
	}
}
func TestAutoStartDaemonRefusesWhenPortInUse(t *testing.T) {
	prev := listenpreflight.Hook
	defer func() { listenpreflight.Hook = prev }()
	listenpreflight.Hook = func(port int) (bool, error) {
		if port != listenpreflight.DaemonPort {
			t.Fatalf("port=%d", port)
		}
		return true, nil
	}
	err := autoStartDaemon()
	if err == nil {
		t.Fatal("expected refusal when port is in use")
	}
	if !strings.Contains(err.Error(), "3030") {
		t.Fatalf("error should mention port: %v", err)
	}
}
