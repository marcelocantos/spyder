// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseForwardList(t *testing.T) {
	out := `emulator-5554 tcp:8080 tcp:9000
R58T61ER0ZB tcp:5555 tcp:5555
emulator-5554 tcp:1234 tcp:4321
`
	all, err := ParseForwardList(out, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("len=%d want 3", len(all))
	}
	filtered, err := ParseForwardList(out, "emulator-5554")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered=%d want 2", len(filtered))
	}
	if filtered[0].LocalPort != 8080 || filtered[0].DevicePort != 9000 {
		t.Errorf("got %+v", filtered[0])
	}
}

func TestForwardTCP_CommandBoundary(t *testing.T) {
	if _, err := exec.LookPath("adb"); err != nil {
		t.Skip("adb not in PATH")
	}
	var calls [][]string
	old := androidAdb
	t.Cleanup(func() { androidAdb = old })
	androidAdb = func(args ...string) (stdout, stderr []byte, err error) {
		calls = append(calls, append([]string{}, args...))
		if len(args) >= 2 && args[2] == "forward" && args[3] == "tcp:0" {
			return []byte("49152\n"), nil, nil
		}
		return nil, nil, nil
	}
	a := NewAndroidAdapter()
	fw, err := a.ForwardTCP("SERIAL", 0, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if fw.LocalPort != 49152 || fw.DevicePort != 8080 {
		t.Errorf("fw=%+v", fw)
	}
	joined := strings.Join(calls[0], " ")
	if !strings.Contains(joined, "forward") || !strings.Contains(joined, "tcp:8080") {
		t.Errorf("args=%v", calls[0])
	}

	if err := a.UnforwardTCP("SERIAL", 49152); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(calls[len(calls)-1], " ")
	if !strings.Contains(last, "--remove") || !strings.Contains(last, "tcp:49152") {
		t.Errorf("unforward args=%v", calls[len(calls)-1])
	}
}

func TestForwardTCP_Validation(t *testing.T) {
	a := NewAndroidAdapter()
	if _, err := a.ForwardTCP("", 1, 2); err == nil {
		t.Error("empty id")
	}
	if _, err := a.ForwardTCP("s", 1, 0); err == nil {
		t.Error("bad device port")
	}
}
