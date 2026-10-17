// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package rest_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios/appservice"
	"github.com/danielpaulus/go-ios/ios/xpc"
	"github.com/marcelocantos/spyder/internal/device"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/rest"
)

type restProcessRead struct {
	conn     *xpc.Connection
	returned chan struct{}
}

func (r *restProcessRead) ListProcesses() ([]appservice.Process, error) {
	defer close(r.returned)
	if err := r.conn.Send(map[string]interface{}{"request": "processes"}); err != nil {
		return nil, err
	}
	_, err := r.conn.ReceiveOnServerClientStream()
	return nil, err
}

type noRestProcesses struct{}

func (noRestProcesses) ListProcesses() ([]appservice.Process, error) { return nil, nil }

func TestRESTIOSProcessCancellation_AfterSocketWrite(t *testing.T) {
	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.WriteFile(paths.InventoryPath(), []byte(`[{"alias":"fixture","platform":"ios","ios_uuid":"fixture-udid"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	c, peer := net.Pipe()
	defer c.Close()
	defer peer.Close()
	xc, err := xpc.New(c, c, c)
	if err != nil {
		t.Fatal(err)
	}
	received, returned := make(chan struct{}), make(chan struct{})
	var acquisitions atomic.Int32
	adapter := device.NewIOSProcessProbeAdapter(device.IOSProcessProbe{
		InstalledExecutable: func(_, _ string) (string, error) { return "/Applications/Fixture.app/Fixture", nil },
		MajorVersion:        func(string) (int, error) { return 27, nil },
		AcquireContext: func(ctx context.Context, _ string) (device.IOSProcessListConn, func(), func(), error) {
			if acquisitions.Add(1) > 1 {
				return noRestProcesses{}, func() {}, func() {}, nil
			}
			closed := make(chan struct{})
			stop := context.AfterFunc(ctx, func() { defer close(closed); _ = xc.Close() })
			return &restProcessRead{xc, returned}, func() {
				if !stop() {
					<-closed
				}
			}, func() {}, nil
		}, Invalidate: func(string) { t.Error("cancelled read must not retry") },
	})
	server := httptest.NewServer(rest.NewHandler(spydermcp.NewHandlerWithAdapters(adapter, nil)))
	defer server.Close()
	go func() {
		if _, err := xpc.DecodeMessage(peer); err == nil {
			close(received)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload := `{"device":"fixture","bundle_id":"com.example.fixture"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+rest.Prefix+"terminate_app", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, err := http.DefaultClient.Do(req)
		if response != nil {
			response.Body.Close()
		}
		if err == nil {
			t.Error("cancelled HTTP request unexpectedly completed normally")
		}
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("request never written on provider socket")
	}
	cancel()
	for _, ch := range []<-chan struct{}{clientDone, returned} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("HTTP cancellation retained socket read")
		}
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Post(server.URL+rest.Prefix+"terminate_app", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("same-device gate blocked: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.IsError {
		t.Fatalf("cleanup result=%s error=%v", body, err)
	}
	if acquisitions.Load() != 3 {
		t.Fatalf("acquisitions=%d", acquisitions.Load())
	}
}
