// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"
	"time"
)

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
