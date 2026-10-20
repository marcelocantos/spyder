// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceKitPinnedConfiguration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ios")
	if err := os.WriteFile(p, []byte("fixture executable"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPYDER_IOS_AGENT_BACKEND", "devicekit")
	t.Setenv("SPYDER_IOS_AGENT_CLI", p)
	t.Setenv("SPYDER_IOS_AGENT_CLI_SHA256", fmt.Sprintf("%x", sha256.Sum256([]byte("fixture executable"))))
	t.Setenv("SPYDER_IOS_AGENT_DEVICES", "00008130-0009702E1110001C")
	config := DeviceKitAgentFromEnvironment()
	if config.err != nil || !config.Devices["00008130-0009702E1110001C"] {
		t.Fatalf("%+v", config)
	}
	if err := os.WriteFile(p, []byte("replacement executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if config.verify() == nil {
		t.Fatal("changed launcher accepted")
	}
	t.Setenv("SPYDER_IOS_AGENT_BACKEND", "unknown")
	if DeviceKitAgentFromEnvironment().err == nil {
		t.Fatal("unknown backend fell back")
	}
	t.Setenv("SPYDER_IOS_AGENT_BACKEND", "devicekit")
	t.Setenv("SPYDER_IOS_AGENT_DEVICES", "")
	if DeviceKitAgentFromEnvironment().err == nil {
		t.Fatal("empty scope accepted")
	}
}

func TestDeviceKitActualHTTPStateRequiresExplicitIdentity(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":"spyder-agent-health","result":{"bundleId":"` + DeviceKitRunnerBundle + `","state":"runningBackground","pid":0}}`
	for _, test := range []struct {
		name, body string
		want       bool
	}{
		{"background", valid, true},
		{"foreground", strings.ReplaceAll(strings.ReplaceAll(valid, "runningBackground", "runningForeground"), `"pid":0`, `"pid":123`), true},
		{"wrong_pid", strings.ReplaceAll(strings.ReplaceAll(valid, "runningBackground", "runningForeground"), `"pid":0`, `"pid":999`), false},
		{"missing_pid", strings.ReplaceAll(valid, `,"pid":0`, ``), false},
		{"wrong_bundle", strings.ReplaceAll(valid, DeviceKitRunnerBundle, "foreign"), false},
		{"wrong_id", strings.ReplaceAll(valid, "spyder-agent-health", "foreign"), false},
		{"not_running", strings.ReplaceAll(valid, "runningBackground", "notRunning"), false},
		{"malformed", "{", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(test.body), test.body)
			if got := deviceKitStateReady(strings.NewReader(wire), 123); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestDeviceKitDeadChannelCannotBeReused(t *testing.T) {
	done := make(chan struct{})
	h := NewAutomationHost(nil)
	h.holds["ipad"] = &runnerHold{pid: 123, done: done}
	if pid, ok := h.heldPID("ipad"); !ok || pid != 123 {
		t.Fatal("live owned channel rejected")
	}
	close(done)
	if _, ok := h.heldPID("ipad"); ok {
		t.Fatal("dead channel reported healthy")
	}
}

func TestDeviceKitRequiresMatchingOwnedChannel(t *testing.T) {
	h := NewAutomationHost(nil)
	h.DeviceKit = &DeviceKitAgent{Devices: map[string]bool{"ipad": true}}
	if h.RunnerBundle() != DeviceKitRunnerBundle {
		t.Fatal("wrong supervised runner identity")
	}
	if _, err := h.Probe("foreign"); err == nil {
		t.Fatal("foreign device admitted")
	}
	if err := h.TapButton("ipad", "Allow"); err == nil {
		t.Fatal("wrong command protocol accepted")
	}
}
