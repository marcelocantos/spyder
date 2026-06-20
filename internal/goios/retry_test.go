// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TunnelInfoForDevice recovers when the registry transiently emits a
// malformed body — the field-observed "unexpected end of JSON" /
// truncated-response symptom during RSD-tunnel settling (🎯T84).
// Three quick attempts at 200ms/500ms/1s backoff cover the typical
// settling window without inflating cold-call latency in the healthy
// case.
func TestTunnelInfoWithRetry_RecoversAfterTransientGarbage(t *testing.T) {
	prev := tunnelInfoBackoffs
	tunnelInfoBackoffs = []time.Duration{
		10 * time.Millisecond,
		10 * time.Millisecond,
		10 * time.Millisecond,
	}
	t.Cleanup(func() { tunnelInfoBackoffs = prev })

	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel/", func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		// First call: emit a deliberately malformed body so the
		// JSON parser yields "unexpected end of JSON input" — the
		// exact symptom the retry exists to absorb.
		if n == 1 {
			_, _ = w.Write([]byte(`{"address":"truncated`))
			return
		}
		_, _ = w.Write([]byte(`{"address":"::1","rsdPort":1234,"udid":"FAKE","userspaceTun":false,"userspaceTunPort":0}`))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	r := New(host, port)

	info, err := r.tunnelInfoWithRetry("FAKE")
	if err != nil {
		t.Fatalf("retry should have recovered after garbage; err=%v", err)
	}
	if info.Address != "::1" || info.RsdPort != 1234 {
		t.Errorf("expected ::1:1234 from second attempt; got %+v", info)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("expected 2 attempts; got %d", got)
	}
}

// Persistent failure: when every attempt fails, the function returns
// the last error so the caller can surface it. Confirms we don't
// silently mask a real daemon outage.
func TestTunnelInfoWithRetry_GivesUpAfterAllFail(t *testing.T) {
	prev := tunnelInfoBackoffs
	tunnelInfoBackoffs = []time.Duration{
		5 * time.Millisecond,
		5 * time.Millisecond,
		5 * time.Millisecond,
	}
	t.Cleanup(func() { tunnelInfoBackoffs = prev })

	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel/", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("not-json"))
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	r := New(host, port)

	_, err = r.tunnelInfoWithRetry("FAKE")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "TunnelInfoForDevice") {
		t.Errorf("error should wrap TunnelInfoForDevice; got %v", err)
	}
	if got := hits.Load(); int(got) != TunnelInfoRetryAttempts {
		t.Errorf("expected %d attempts; got %d", TunnelInfoRetryAttempts, got)
	}
}

// silenceUnusedFmt keeps the fmt import live even when error
// formatting refactors remove its only inline use.
var _ = fmt.Sprintf
