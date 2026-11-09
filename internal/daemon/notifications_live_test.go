// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/notify"
	"github.com/marcelocantos/spyder/internal/paths"
)

// Property: the real CLI/daemon delivers an already-connected physical device
// event to a live T3 webhook, using provider event waits and private persistence.
// Supply a temporary, thread-bound T3 task with replayKey={{body.event_id}}.
// The task must have no signature/filters/coalescing. No device is mutated.
func TestDeviceNotification_Live(t *testing.T) {
	if os.Getenv("SPYDER_NOTIFY_LIVE") != "1" {
		t.Skip("make test-live-notify supplies a live device and T3 callback")
	}
	binary, err := filepath.Abs(os.Getenv("SPYDER_LIVE_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	deviceID, platform := os.Getenv("SPYDER_NOTIFY_DEVICE_ID"), os.Getenv("SPYDER_NOTIFY_PLATFORM")
	generic := os.Getenv("SPYDER_NOTIFY_EVENT_LIVE") == "1"
	genericDevice := os.Getenv("SPYDER_NOTIFY_DEVICE_EVENT_LIVE") == "1"
	if !generic && (deviceID == "" || (platform != "ios" && platform != "android")) {
		t.Fatal("set device hardware ID and platform")
	}
	webhook, err := os.ReadFile(os.Getenv("SPYDER_NOTIFY_WEBHOOK_FILE"))
	if err != nil {
		t.Fatal("read webhook credential file:", err)
	}
	root := t.TempDir()
	logFile, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(addr, ":3030") {
		t.Fatal("refusing daily-driver port")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "serve", "--addr", addr)
	cmd.Env = append(os.Environ(), paths.DataDirEnv+"="+root, "SPYDER_IOS_TUNNEL_BINARY="+filepath.Join(root, "no-tunnel"))
	cmd.Dir = root
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cancel()
			<-done
			t.Error("daemon failed to stop")
		}
		b, err := os.ReadFile(logFile.Name())
		if err != nil {
			t.Error(err)
		} else if strings.Contains(string(b), strings.TrimSpace(string(webhook))) {
			t.Error("callback URL leaked into daemon log")
		}
	})
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := client.Get("http://" + addr + "/api/v1/health")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated daemon unavailable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	run := func(input string, args ...string) []byte {
		t.Helper()
		callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		cli := exec.CommandContext(callCtx, binary, args...)
		cli.Env = append(os.Environ(), "SPYDER_DAEMON_URL=http://"+addr)
		cli.Dir = root
		cli.Stdin = strings.NewReader(input)
		b, err := cli.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %s failed: %v %s", args[0], err, b)
		}
		if strings.Contains(string(b), strings.TrimSpace(string(webhook))) {
			t.Fatal("callback URL leaked into CLI output")
		}
		return b
	}
	registeredAt := time.Now()
	var b []byte
	if generic {
		b = run(string(webhook), "notify", "subscribe", "verify.completed", "--match", "workflow=notification-live", "--match", "status=passed", "--recurring", "--webhook-url", "-", "--ttl", "1m")
	} else if genericDevice {
		b = run(string(webhook), "notify", "subscribe", "device.connected", "--match", "device_id="+deviceID, "--match", "platform="+platform, "--webhook-url", "-", "--ttl", "1m")
	} else {
		b = run(string(webhook), "notify", "connected", deviceID, "--platform", platform, "--webhook-url", "-", "--ttl", "1m")
	}
	var subscription notify.Subscription
	if err := json.Unmarshal(b, &subscription); err != nil {
		t.Fatalf("decode registration: %v %s", err, b)
	}
	if subscription.ID == "" {
		t.Fatal("no notification ID")
	}
	if generic {
		workflow := filepath.Join(root, "event-workflow.yaml")
		if err := os.WriteFile(workflow, []byte("name: notification-live\nsteps:\n  - id: ready\n    type: shell\n    argv: [/usr/bin/true]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		var eventIDs []string
		for i := 1; i <= 2; i++ {
			run("", "verify", workflow, "--cwd", root, "--as", "notification-journey", "--json")
			deadline = time.Now().Add(15 * time.Second)
			for {
				var rows []notify.Subscription
				if err := json.Unmarshal(run("", "notify", "list"), &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 {
					t.Fatalf("unexpected subscriptions: %+v", rows)
				}
				if rows[0].Delivered == i && rows[0].State == "waiting" {
					if rows[0].LastEventID == "" || rows[0].Dropped != 0 || rows[0].Pending != 0 {
						t.Fatalf("bad recurring delivery: %+v", rows)
					}
					eventIDs = append(eventIDs, rows[0].LastEventID)
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("recurring callback not accepted: %+v", rows)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		if eventIDs[0] == eventIDs[1] {
			t.Fatal("distinct occurrences share a replay key")
		}
		run("", "notify", "cancel", subscription.ID)
		if receipt := os.Getenv("SPYDER_NOTIFY_RECEIPT_FILE"); receipt != "" {
			data, err := json.Marshal(eventIDs)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(receipt, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("live recurring verify.completed callbacks accepted: events=%v elapsed=%s", eventIDs, time.Since(registeredAt))
		return
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		var rows []notify.Subscription
		if err := json.Unmarshal(run("", "notify", "list"), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("unexpected registrations: %+v", rows)
		}
		if rows[0].State == "delivered" {
			if rows[0].ID != subscription.ID || rows[0].Attempts != 1 || (!genericDevice && rows[0].ConnectedAt == nil) || (genericDevice && rows[0].LastEventID == "") {
				t.Fatalf("bad callback receipt: %+v", rows)
			}
			eventID := subscription.ID
			if genericDevice {
				eventID = rows[0].LastEventID
			}
			t.Logf("live T3 callback accepted: event_id=%s platform=%s device=%s elapsed=%s", eventID, platform, deviceID, time.Since(registeredAt))
			if receipt := os.Getenv("SPYDER_NOTIFY_RECEIPT_FILE"); receipt != "" {
				data, err := json.Marshal(rows[0])
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(receipt, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("T3 callback not accepted: %+v", rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
