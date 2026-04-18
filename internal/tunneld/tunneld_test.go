// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package tunneld

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stripScheme returns addr in host:port form (httptest.Server.URL has
// an http:// prefix we need to drop).
func stripScheme(url string) string {
	return strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
}

func TestProbe_Valid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"00008103-000D39301A6A201E": [{"tunnel-address": "fd28::1", "tunnel-port": 57990}],
			"R5CR112X76K":                [{"tunnel-address": "fdXX::2", "tunnel-port": 12345}]
		}`))
	}))
	defer srv.Close()

	c := New(stripScheme(srv.URL))
	udids, err := c.Probe()
	if err != nil {
		t.Fatalf("Probe err = %v", err)
	}
	if len(udids) != 2 {
		t.Fatalf("got %d UDIDs; want 2 (%v)", len(udids), udids)
	}
	// Order is map-iteration, so we just check both are present.
	have := map[string]bool{}
	for _, u := range udids {
		have[u] = true
	}
	for _, want := range []string{"00008103-000D39301A6A201E", "R5CR112X76K"} {
		if !have[want] {
			t.Errorf("missing UDID %q in %v", want, udids)
		}
	}
}

func TestProbe_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(stripScheme(srv.URL))
	udids, err := c.Probe()
	if err != nil {
		t.Fatalf("Probe on empty body err = %v", err)
	}
	if len(udids) != 0 {
		t.Errorf("got %d UDIDs; want 0", len(udids))
	}
}

func TestProbe_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := New(stripScheme(srv.URL))
	_, err := c.Probe()
	if err == nil {
		t.Fatal("Probe with bad JSON returned nil err; want error")
	}
	if !strings.Contains(err.Error(), "parsing tunneld response") {
		t.Errorf("err = %v; want parse error", err)
	}
}

func TestProbe_500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(stripScheme(srv.URL))
	_, err := c.Probe()
	if err == nil {
		t.Fatal("Probe on 500 returned nil err; want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v; want 500 mention", err)
	}
}

func TestProbe_Unreachable(t *testing.T) {
	// Port 1 is reserved and not listening.
	c := New("127.0.0.1:1")
	_, err := c.Probe()
	if err == nil {
		t.Fatal("Probe on unreachable returned nil err; want error")
	}
	if !strings.Contains(err.Error(), "tunneld unreachable") {
		t.Errorf("err = %v; want 'tunneld unreachable'", err)
	}
}

func TestRequire_WrapsErrUnavailable(t *testing.T) {
	c := New("127.0.0.1:1")
	err := c.Require()
	if err == nil {
		t.Fatal("Require on unreachable returned nil; want error")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err does not wrap ErrUnavailable: %v", err)
	}
}

func TestAddr(t *testing.T) {
	c := New("10.0.0.1:9999")
	if got := c.Addr(); got != "10.0.0.1:9999" {
		t.Errorf("Addr = %q; want 10.0.0.1:9999", got)
	}
}
