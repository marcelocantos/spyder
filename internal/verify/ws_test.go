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

func TestHandleReportServesPlainJSON(t *testing.T) {
	hub := NewHub(HubArgs{})
	res := runWF(t, hub, "name: report-url\nsteps:\n  - {id: a, type: shell, argv: [/usr/bin/true]}\n", "", nil)
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleReport))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + ReportPathPrefix + res.RunID + "/report.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rep Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil || resp.StatusCode != 200 || rep.RunID != res.RunID || rep.Status != StatusPassed || len(rep.Steps) != 1 {
		t.Fatalf("status=%d err=%v report=%+v", resp.StatusCode, err, rep)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
	for _, path := range []string{"nope/report.json", res.RunID + "/other.json", "a/b/report.json"} {
		r, err := http.Get(srv.URL + ReportPathPrefix + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status %d", path, r.StatusCode)
		}
	}
}

// 🎯T147: GET /verify/runs/<id>/report.json is a stable link — it still
// serves a dismissed run's bundle from disk, and a path-like run id is a
// plain 404, never a filesystem read.
func TestHandleReportServesDismissedRunByLink(t *testing.T) {
	state := t.TempDir()
	hub := NewHub(HubArgs{StateDir: state})
	res := runWF(t, hub, "name: dismissed-report-url\nsteps:\n  - {id: a, type: shell, argv: [/usr/bin/true]}\n", "", nil)
	if err := hub.Dismiss(res.RunID, res.Owner); err != nil {
		t.Fatal(err)
	}
	if views := hub.Snapshot().Runs; len(views) != 0 {
		t.Fatalf("dismissed run still live: %+v", views)
	}

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleReport))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + ReportPathPrefix + res.RunID + "/report.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rep Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil || resp.StatusCode != 200 || rep.RunID != res.RunID || rep.Status != StatusPassed {
		t.Fatalf("status=%d err=%v report=%+v", resp.StatusCode, err, rep)
	}
	if views := hub.Snapshot().Runs; len(views) != 0 {
		t.Fatalf("GET report.json resurrected the dismissed run onto the dashboard: %+v", views)
	}

	for _, id := range []string{"../../../../etc/passwd", "..%2F..%2Fetc%2Fpasswd", "nonexistent-run-id"} {
		r, err := http.Get(srv.URL + ReportPathPrefix + id + "/report.json")
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("path-like run id %q: status %d", id, r.StatusCode)
		}
	}
}
