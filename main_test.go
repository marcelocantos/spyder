// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"
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
			dev, cmd, err := parseRunArgs(c.args)
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
			if dev != c.wantDev {
				t.Errorf("dev = %q; want %q", dev, c.wantDev)
			}
			if !reflect.DeepEqual(cmd, c.wantCmd) {
				t.Errorf("cmd = %v; want %v", cmd, c.wantCmd)
			}
		})
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
