// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGed serves the confirmed ged shapes for /api/info and /api/tweaks,
// plus the tweak POST routes, so the client can be tested headlessly.
func fakeGed(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"connected":false,"servers":[],"sessions":0}`))
	})
	mux.HandleFunc("GET /api/tweaks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	// With no app attached, ged returns 503 on these — mirror that.
	mux.HandleFunc("POST /api/tweaks", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no server connected"}`, http.StatusServiceUnavailable)
	})
	mux.HandleFunc("POST /api/tweaks/reset", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no server connected"}`, http.StatusServiceUnavailable)
	})
	return httptest.NewServer(mux)
}

func TestClientInfo(t *testing.T) {
	srv := fakeGed(t)
	defer srv.Close()

	raw, err := NewClient(srv.URL).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Connected bool  `json:"connected"`
		Servers   []any `json:"servers"`
		Sessions  int   `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("parse info: %v", err)
	}
	if info.Connected || len(info.Servers) != 0 || info.Sessions != 0 {
		t.Errorf("want empty state, got %+v", info)
	}
}

func TestClientTweaks(t *testing.T) {
	srv := fakeGed(t)
	defer srv.Close()

	raw, err := NewClient(srv.URL).Tweaks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var tweaks []any
	if err := json.Unmarshal(raw, &tweaks); err != nil {
		t.Fatalf("parse tweaks: %v", err)
	}
	if len(tweaks) != 0 {
		t.Errorf("want empty tweaks, got %v", tweaks)
	}
}

func TestClientTrailingSlashTolerated(t *testing.T) {
	srv := fakeGed(t)
	defer srv.Close()
	if _, err := NewClient(srv.URL + "/").Info(context.Background()); err != nil {
		t.Errorf("trailing slash should be tolerated: %v", err)
	}
}

func TestClientNon2xxIsError(t *testing.T) {
	srv := fakeGed(t)
	defer srv.Close()
	c := NewClient(srv.URL)

	err := c.TweakSet(context.Background(), "camera.fov_deg", 60)
	if err == nil {
		t.Fatal("expected error on 503 tweak_set")
	}
	if !strings.Contains(err.Error(), "503") && !strings.Contains(err.Error(), "Service Unavailable") {
		t.Errorf("error should carry the status, got %v", err)
	}

	if err := c.TweakReset(context.Background(), ""); err == nil {
		t.Fatal("expected error on 503 tweak_reset")
	}
}

func TestClientUnknownRouteIsError(t *testing.T) {
	// A bare server with no routes returns 404 for /api/info.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := NewClient(srv.URL).Info(context.Background()); err == nil {
		t.Error("expected error on 404")
	}
}

func TestClientLogsUnavailable(t *testing.T) {
	c := NewClient("http://localhost:1")
	_, err := c.Logs(context.Background(), 20)
	if !errors.Is(err, ErrLogsUnavailable) {
		t.Errorf("Logs should return ErrLogsUnavailable, got %v", err)
	}
}

func TestClientTweakSetBody(t *testing.T) {
	// Assert the client posts {"name":..,"value":..} — the shape ged's
	// dashboard route wraps into {"type":"tweak_set","data":<body>}.
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	if err := NewClient(srv.URL).TweakSet(context.Background(), "camera.fov_deg", 42.0); err != nil {
		t.Fatal(err)
	}
	if gotBody["name"] != "camera.fov_deg" || gotBody["value"] != 42.0 {
		t.Errorf("unexpected tweak_set body: %v", gotBody)
	}
}

func TestClientTweakResetBodies(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	if err := c.TweakReset(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"all":true`) {
		t.Errorf("reset-all should send {all:true}, got %q", gotBody)
	}
	if err := c.TweakReset(context.Background(), "camera.fov_deg"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"name":"camera.fov_deg"`) {
		t.Errorf("named reset should send {name:..}, got %q", gotBody)
	}
}
