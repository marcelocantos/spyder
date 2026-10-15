// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
)

// Opt-in shipped REST/daemon/DesktopAdapter gate. The fixture is a real
// process which deliberately never opens the app channel; no phone or owner
// application is installed, launched or terminated. Run via the Make target.
func TestEnsureSessionCancellationDesktop_Live(t *testing.T) {
	binary := os.Getenv("SPYDER_LIVE_BINARY")
	if binary == "" {
		t.Skip("set SPYDER_LIVE_BINARY; make test-live-session-cancel runs the real daemon/provider gate")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	address := os.Getenv("SPYDER_LIVE_ADDR")
	if address == "" {
		address = "127.0.0.1:3132"
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "3030" || port == "3131" {
		t.Fatalf("require isolated IPv4 loopback port, excluding3030/3131: %q", address)
	}
	root := t.TempDir()
	source := filepath.Join(root, "fixture.go")
	fixture := filepath.Join(root, "fixture")
	other := filepath.Join(root, "other-fixture")
	pidFile := filepath.Join(root, "fixture.pid")
	fixtureCode := `package main
import("context";"os";"os/signal";"strconv";"syscall";"time")
func main(){if p:=os.Getenv("SPYDER_FIXTURE_PID_FILE");p!=""{if err:=os.WriteFile(p,[]byte(strconv.Itoa(os.Getpid())),0600);err!=nil{panic(err)}};ctx,cancel:=signal.NotifyContext(context.Background(),syscall.SIGTERM,syscall.SIGINT);defer cancel();select{case <-ctx.Done():case <-time.After(30*time.Second):}}
`
	if err := os.WriteFile(source, []byte(fixtureCode), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", fixture, source)
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, out)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, data, 0700); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	inventory := fmt.Sprintf(`[{"alias":"fixture","platform":"desktop","executable_path":%q},{"alias":"other","platform":"desktop","executable_path":%q}]`, fixture, other)
	if err := os.WriteFile(filepath.Join(dataDir, "inventory.json"), []byte(inventory), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(binary, "serve", "--addr", address)
	cmd.Env = append(os.Environ(), paths.DataDirEnv+"="+dataDir, "SPYDER_IOS_TUNNEL_BINARY="+filepath.Join(root, "no-tunnel"))
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	daemonDone := make(chan struct{})
	var daemonErr error
	go func() { daemonErr = cmd.Wait(); close(daemonDone) }()
	client := &http.Client{Timeout: 2 * time.Second}
	call := func(verb string, args map[string]any) (map[string]any, error) {
		body, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		response, err := client.Post("http://"+address+"/api/v1/"+verb, "application/json", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		var envelope map[string]any
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK || envelope["isError"] == true {
			return envelope, fmt.Errorf("REST %s failed: %v", verb, envelope)
		}
		return envelope, nil
	}
	var fixturePID int
	t.Cleanup(func() {
		_, _ = call("terminate_app", map[string]any{"device": "fixture", "bundle_id": "fixture.cancel", "owner": "test"})
		if fixturePID > 0 && syscall.Kill(fixturePID, 0) == nil {
			_ = syscall.Kill(fixturePID, syscall.SIGTERM)
			t.Error("fixture required fallback cleanup")
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-daemonDone:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-daemonDone
			t.Error("daemon did not shut down promptly")
		}
		t.Logf("temporary daemon log: %s", log.Name())
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := call("device_state", map[string]any{"device": "other"}); err == nil {
			break
		}
		select {
		case <-daemonDone:
			t.Fatalf("daemon exited during startup: %v", daemonErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon startup timed out")
		}
		time.Sleep(25 * time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"device": "fixture", "bundle_id": "fixture.cancel", "owner": "test", "timeout_ms": 90000, "env": map[string]string{"SPYDER_FIXTURE_PID_FILE": pidFile}})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/v1/ensure_session", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	requestDone := make(chan error, 1)
	go func() {
		r, err := http.DefaultClient.Do(request)
		if r != nil {
			r.Body.Close()
		}
		requestDone <- err
	}()
	deadline = time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(pidFile); err == nil {
			fixturePID, err = strconv.Atoi(string(data))
			if err != nil || fixturePID <= 0 {
				t.Fatalf("bad fixturePID: %q", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real fixture process did not start")
		}
		time.Sleep(25 * time.Millisecond)
	}
	start := time.Now()
	if _, err := call("device_state", map[string]any{"device": "other"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("other-device status while90s wait active: %s", time.Since(start))
	cancel()
	select {
	case err := <-requestDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled real HTTP request did not return")
	}
	start = time.Now()
	if _, err := call("terminate_app", map[string]any{"device": "fixture", "bundle_id": "fixture.cancel", "owner": "test"}); err != nil {
		t.Fatalf("same-device terminate did not acquire released gate: %v", err)
	}
	t.Logf("same-device terminate after cancellation: %s", time.Since(start))
	if syscall.Kill(fixturePID, 0) == nil {
		t.Fatal("real fixture process remains alive after termination")
	}
	// The old handler caused a daemon exit after its five-second recovery
	// grace, even though unrelated devices were still answering normally.
	select {
	case <-daemonDone:
		t.Fatalf("daemon exited after cancellation: %v", daemonErr)
	case <-time.After(6 * time.Second):
	}
	if _, err := call("device_state", map[string]any{"device": "other"}); err != nil {
		t.Fatalf("daemon did not survive cancellation/recovery grace: %v", err)
	}
	t.Log("real REST cancellation released the lifecycle gate; fixture stopped; daemon survived recovery grace")
}
