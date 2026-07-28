// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInjectTapSwipe_CommandBoundary(t *testing.T) {
	if _, err := exec.LookPath("adb"); err != nil {
		t.Skip("adb not in PATH")
	}
	var calls [][]string
	old := androidAdb
	t.Cleanup(func() { androidAdb = old })
	androidAdb = func(args ...string) (stdout, stderr []byte, err error) {
		calls = append(calls, append([]string{}, args...))
		return nil, nil, nil
	}
	a := NewAndroidAdapter()
	if err := a.InjectTap("SERIAL", 100, 200); err != nil {
		t.Fatal(err)
	}
	if err := a.InjectSwipe("SERIAL", 10, 20, 30, 40, 150); err != nil {
		t.Fatal(err)
	}
	if err := a.InjectSwipe("SERIAL", 1, 2, 3, 4, 0); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls=%d", len(calls))
	}
	tap := strings.Join(calls[0], " ")
	if !strings.Contains(tap, "input tap 100 200") {
		t.Errorf("tap=%v", calls[0])
	}
	swipe := strings.Join(calls[1], " ")
	if !strings.Contains(swipe, "input swipe 10 20 30 40 150") {
		t.Errorf("swipe=%v", calls[1])
	}
	def := strings.Join(calls[2], " ")
	if !strings.Contains(def, "300") {
		t.Errorf("default duration swipe=%v", calls[2])
	}
}

func TestInject_Validation(t *testing.T) {
	a := NewAndroidAdapter()
	if err := a.InjectTap("", 1, 1); err == nil {
		t.Error("empty id")
	}
	if err := a.InjectTap("s", -1, 0); err == nil {
		t.Error("neg x")
	}
	if err := a.InjectSwipe("s", -1, 0, 0, 0, 1); err == nil {
		t.Error("neg coord")
	}
}
