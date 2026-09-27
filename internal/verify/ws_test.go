// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const waitingGateYAML = `
name: live-ws
steps:
  - id: ask
    type: human_gate
    prompt: Look
    choices:
      - id: pass
        label: Yes
`

func TestHub_SubscribeWakesOnStart(t *testing.T) {
	hub := NewHub(HubArgs{})
	ch, unsub := hub.subscribe()
	defer unsub()

	run, err := hub.Start(context.Background(), StartArgs{
		Workflow: loadWF(t, waitingGateYAML),
		Cwd:      t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, hub, run)

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber was not woken when a run started")
	}
}

func TestHub_WSPushesRunWithoutPolling(t *testing.T) {
	hub := NewHub(HubArgs{})
	mux := http.NewServeMux()
	mux.HandleFunc(WSPath, hub.HandleWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	t.Cleanup(cancel)

	c, _, err := websocket.Dial(ctx, httpToWS(srv.URL)+WSPath, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })

	first := readSnapshot(t, ctx, c)
	if len(first.Runs) != 0 {
		t.Fatalf("fresh hub snapshot has %d runs", len(first.Runs))
	}

	run, err := hub.Start(context.Background(), StartArgs{
		Workflow: loadWF(t, waitingGateYAML),
		Cwd:      t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, hub, run)

	snap := waitSnapshot(t, ctx, c, func(s Snapshot) bool {
		return snapshotRun(s, run.ID) != nil
	})
	got := snapshotRun(snap, run.ID)
	if got.Status != "running" {
		t.Fatalf("pushed status = %q, want running", got.Status)
	}
	if runningCount(snap) != 1 {
		t.Fatalf("running count = %d, want 1", runningCount(snap))
	}
}

func TestHub_WSLateSubscriberGetsCurrentRun(t *testing.T) {
	hub := NewHub(HubArgs{})
	mux := http.NewServeMux()
	mux.HandleFunc(WSPath, hub.HandleWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	run, err := hub.Start(context.Background(), StartArgs{
		Workflow: loadWF(t, waitingGateYAML),
		Cwd:      t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, hub, run)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, httpToWS(srv.URL)+WSPath, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })

	snap := readSnapshot(t, ctx, c)
	if snapshotRun(snap, run.ID) == nil {
		t.Fatalf("late subscriber first frame missing run %s: %+v", run.ID, snap)
	}
}

func stopRun(t *testing.T, hub *Hub, run *Run) {
	t.Helper()
	t.Cleanup(func() {
		_ = hub.Abort(run.ID, "test done")
		run.Wait()
	})
}

func httpToWS(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func readSnapshot(t *testing.T, ctx context.Context, c *websocket.Conn) Snapshot {
	t.Helper()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("ws type %v, want text", typ)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot json: %v", err)
	}
	return snap
}

func waitSnapshot(t *testing.T, ctx context.Context, c *websocket.Conn, ok func(Snapshot) bool) Snapshot {
	t.Helper()
	for {
		snap := readSnapshot(t, ctx, c)
		if ok(snap) {
			return snap
		}
	}
}

func snapshotRun(snap Snapshot, id string) *RunView {
	for i := range snap.Runs {
		if snap.Runs[i].RunID == id {
			return &snap.Runs[i]
		}
	}
	return nil
}

func runningCount(snap Snapshot) int {
	n := 0
	for _, r := range snap.Runs {
		if r.Status == "running" {
			n++
		}
	}
	return n
}
