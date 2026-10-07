// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
package device

import (
	"reflect"
	"testing"
)

func TestReverseTCPCommands(t *testing.T) {
	old := androidAdb
	t.Cleanup(func() { androidAdb = old })
	var calls [][]string
	androidAdb = func(args ...string) ([]byte, []byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, nil, nil
	}
	a := NewAndroidAdapter()
	if err := a.ReverseTCP("SERIAL", 8080); err != nil {
		t.Fatal(err)
	}
	if err := a.UnreverseTCP("SERIAL", 8080); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls[0], []string{"-s", "SERIAL", "reverse", "tcp:8080", "tcp:8080"}) {
		t.Fatalf("reverse: %v", calls[0])
	}
	if !reflect.DeepEqual(calls[1], []string{"-s", "SERIAL", "reverse", "--remove", "tcp:8080"}) {
		t.Fatalf("remove: %v", calls[1])
	}
}
