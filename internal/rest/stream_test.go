// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package rest_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/network"
	"github.com/marcelocantos/spyder/internal/rest"
)

// stubStreamAdapter satisfies device.Adapter. LogStream sends a fixed
// set of lines then returns when ctx is done or lines are exhausted.
type stubStreamAdapter struct {
	lines []device.LogLine
}

func (s *stubStreamAdapter) List() ([]device.Info, error)                 { return nil, nil }
func (s *stubStreamAdapter) State(id string) (device.State, error)        { return device.State{}, nil }
func (s *stubStreamAdapter) LaunchKeepAwake(id string) error              { return nil }
func (s *stubStreamAdapter) Screenshot(id string) ([]byte, error)         { return nil, nil }
func (s *stubStreamAdapter) ListApps(id string) ([]device.AppInfo, error) { return nil, nil }
func (s *stubStreamAdapter) LaunchApp(id, b string) error                 { return nil }
func (s *stubStreamAdapter) TerminateApp(id, b string) error              { return nil }
func (s *stubStreamAdapter) InstallApp(id, p string) error                { return nil }
func (s *stubStreamAdapter) UninstallApp(id, b string) error              { return nil }
func (s *stubStreamAdapter) AppPID(id, b string) (int, error)             { return 0, nil }
func (s *stubStreamAdapter) Rotate(id, o string) error                    { return nil }
func (s *stubStreamAdapter) Crashes(id string, _ time.Time, _ string) ([]device.CrashReport, error) {
	return nil, nil
}
func (s *stubStreamAdapter) StartRecording(id, dest string) (func() error, int, error) {
	return func() error { return nil }, 0, nil
}
func (s *stubStreamAdapter) StopRecording(id string, pid int) error                 { return nil }
func (s *stubStreamAdapter) ApplyNetwork(id string, p network.NetworkProfile) error { return nil }
func (s *stubStreamAdapter) ClearNetwork(id string) error                           { return nil }
func (s *stubStreamAdapter) LogRange(id string, _ device.LogFilter, _, _ time.Time) ([]device.LogLine, error) {
	return s.lines, nil
}
func (s *stubStreamAdapter) LogStream(ctx context.Context, id string, _ device.LogFilter, out chan<- device.LogLine) error {
	for _, ll := range s.lines {
		select {
		case out <- ll:
		case <-ctx.Done():
			return nil
		}
	}
	return nil
}

// newStreamTestServer wires up a handler with a stub iOS adapter that
// returns the given log lines.
func newStreamTestServer(t *testing.T, lines []device.LogLine) (string, func()) {
	t.Helper()
	stub := &stubStreamAdapter{lines: lines}
	h := spydermcp.NewHandlerWithAdapters(nil, stub, nil)
	ts := httptest.NewServer(rest.NewHandler(h))
	return ts.URL, ts.Close
}

func TestLogStream_SSE_HappyPath(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	lines := []device.LogLine{
		{Timestamp: now, Process: "MyApp", Level: "info", Message: "hello"},
		{Timestamp: now.Add(time.Second), Process: "MyApp", Level: "error", Message: "world"},
	}
	base, teardown := newStreamTestServer(t, lines)
	defer teardown()

	body, _ := json.Marshal(map[string]any{"device": "00008103-000D39301A6A201E"})
	resp, err := http.Post(base+rest.StreamPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q; want text/event-stream", ct)
	}

	var received []device.LogLine
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var ll device.LogLine
		if err := json.Unmarshal([]byte(data), &ll); err != nil {
			t.Fatalf("unmarshal SSE event %q: %v", data, err)
		}
		received = append(received, ll)
	}

	if len(received) != 2 {
		t.Fatalf("received %d events; want 2", len(received))
	}
	if received[0].Message != "hello" {
		t.Errorf("event[0].Message = %q; want hello", received[0].Message)
	}
	if received[1].Message != "world" {
		t.Errorf("event[1].Message = %q; want world", received[1].Message)
	}
}

func TestLogStream_MissingDevice_Returns400(t *testing.T) {
	base, teardown := newStreamTestServer(t, nil)
	defer teardown()

	resp, err := http.Post(base+rest.StreamPath, "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

func TestLogStream_WrongMethod_Returns405(t *testing.T) {
	base, teardown := newStreamTestServer(t, nil)
	defer teardown()

	resp, err := http.Get(base + rest.StreamPath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", resp.StatusCode)
	}
}
