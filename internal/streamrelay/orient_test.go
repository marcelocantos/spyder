// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package streamrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseHelloOrientation(t *testing.T) {
	cases := map[string]string{
		`{"type":"hello","orientation":"landscape"}`: "landscape",
		`{"type":"hello","orientation":"PORTRAIT"}`:  "portrait",
		`{"type":"hello"}`:                           "",
		`not-json`:                                   "",
	}
	for in, want := range cases {
		if got := parseHelloOrientation([]byte(in)); got != want {
			t.Errorf("parseHelloOrientation(%s)=%q want %q", in, got, want)
		}
	}
}

func TestRelay_HelloSetsOrientation(t *testing.T) {
	relay := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/server", relay.HandleServerSideband)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sb, _, err := websocket.Dial(ctx, base+"/ws/server?name=game1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(websocket.StatusNormalClosure, "") })
	if !waitFor(2*time.Second, func() bool { return len(relay.Servers()) == 1 }) {
		t.Fatal("not registered")
	}
	if err := sb.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","orientation":"landscape"}`)); err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Second, func() bool { return relay.ServerOrientation("game1") == "landscape" }) {
		t.Fatalf("orientation=%q", relay.ServerOrientation("game1"))
	}
}
