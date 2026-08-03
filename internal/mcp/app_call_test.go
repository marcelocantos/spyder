// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// App-advertised method discovery (app_methods) and generic invoke (app_call).

package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func TestAppMethodsAndCall_DiscoverAndInvoke(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmokeHello(t, port, appchannel.Hello{
		AppName:    "yourworld-smoke",
		AppVersion: "9.9",
		Methods: []appchannel.MethodDescriptor{
			{Name: appchannel.MethodPing},
			{
				Name:          "select_country",
				ExampleParams: map[string]any{"id": "FR"},
				Doc:           "Select country by ISO id",
			},
			{
				Name:          "globe_look_at",
				ExampleParams: map[string]any{"lon": 2.35, "lat": 48.86, "zoom": 4.0},
			},
		},
	})
	defer client.close()
	sid := waitForAppSession(t, h)

	// Discovery — agent sees possibility + actual surface.
	r := dispatchJSON(t, h, "app_methods", map[string]any{"session_id": sid})
	if r.IsError {
		t.Fatalf("app_methods: %s", resultText(t, &r))
	}
	body := dispatchJSONMap(t, h, "app_methods", map[string]any{"session_id": sid})
	if body["app_name"] != "yourworld-smoke" {
		t.Fatalf("app_name=%v", body["app_name"])
	}
	methods, ok := body["methods"].([]any)
	if !ok || len(methods) < 3 {
		t.Fatalf("methods=%v", body["methods"])
	}
	byName := map[string]map[string]any{}
	for _, el := range methods {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		byName[name] = m
	}
	if byName["ping"]["kind"] != "engine" {
		t.Errorf("ping kind=%v want engine", byName["ping"]["kind"])
	}
	sel := byName["select_country"]
	if sel["kind"] != "app" {
		t.Errorf("select_country kind=%v want app", sel["kind"])
	}
	if sel["doc"] != "Select country by ISO id" {
		t.Errorf("doc=%v", sel["doc"])
	}
	ex, ok := sel["example_params"].(map[string]any)
	if !ok || ex["id"] != "FR" {
		t.Errorf("example_params=%v", sel["example_params"])
	}

	// scope=app hides engine builtins
	appOnly := dispatchJSONMap(t, h, "app_methods", map[string]any{
		"session_id": sid, "scope": "app",
	})
	for _, el := range appOnly["methods"].([]any) {
		m := el.(map[string]any)
		if m["kind"] != "app" {
			t.Errorf("scope=app leaked %v", m)
		}
	}

	// Invoke game method via generic pass-through.
	call := dispatchJSONMap(t, h, "app_call", map[string]any{
		"session_id": sid,
		"method":     "select_country",
		"params":     map[string]any{"id": "DE"},
	})
	if call["method"] != "select_country" {
		t.Fatalf("call method=%v", call["method"])
	}
	res, ok := call["result"].(map[string]any)
	if !ok || res["ok"] != true {
		t.Fatalf("result=%v", call["result"])
	}
	if client.lastAppCallMethod != "select_country" {
		t.Fatalf("smoke saw method=%q", client.lastAppCallMethod)
	}
	echo, ok := client.lastAppCallParams.(map[string]any)
	if !ok || echo["id"] != "DE" {
		t.Fatalf("smoke params=%v", client.lastAppCallParams)
	}
}

func TestAppCall_NotAdvertisedFailsClosed(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmoke(t, port, []string{appchannel.MethodPing})
	defer client.close()
	sid := waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_call", map[string]any{
		"session_id": sid,
		"method":     "select_country",
		"params":     map[string]any{"id": "FR"},
	})
	if !r.IsError {
		t.Fatalf("expected error, got %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "does not advertise") {
		t.Errorf("want advertise error, got: %s", body)
	}
}

func TestAppCall_MethodRequired(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmoke(t, port, []string{appchannel.MethodPing})
	defer client.close()
	sid := waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_call", map[string]any{"session_id": sid})
	if !r.IsError {
		t.Fatal("expected method required")
	}
	if !strings.Contains(resultText(t, &r), "method") {
		t.Errorf("got: %s", resultText(t, &r))
	}
}

// 🎯T115: params is the one documented bag name; args (or anything else)
// is rejected with an error that names params and shows example_params.
func TestAppCall_RejectsArgsBagName(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmokeHello(t, port, appchannel.Hello{
		Methods: []appchannel.MethodDescriptor{
			{Name: appchannel.MethodPing},
			{Name: "select_country", ExampleParams: map[string]any{"id": "FR"}},
		},
	})
	defer client.close()
	sid := waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_call", map[string]any{
		"session_id": sid,
		"method":     "select_country",
		"args":       map[string]any{"id": "FR"},
	})
	if !r.IsError {
		t.Fatalf("args= must be rejected; got %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	for _, want := range []string{
		"unknown argument args",
		"params={...}",
		`{"id":"FR"}`, // example_params rides the error
	} {
		if !strings.Contains(body, want) {
			t.Errorf("error missing %q; got: %s", want, body)
		}
	}
	if client.lastAppCallMethod != "" {
		t.Errorf("rejected call must not reach the app; app saw %q", client.lastAppCallMethod)
	}
}

// 🎯T123: a failed app_call names the method, shows the advertised
// example_params, and summarises the payload actually sent.
func TestAppCall_FailureNamesMethodExampleAndPayload(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmokeHello(t, port, appchannel.Hello{
		Methods: []appchannel.MethodDescriptor{
			{Name: appchannel.MethodPing},
			{Name: "start_mode", ExampleParams: map[string]any{"mode": "sandbox"}},
		},
	})
	defer client.close()
	sid := waitForAppSession(t, h)

	// Missing the required "mode" key → smoke app returns an RPC error.
	r := dispatchJSON(t, h, "app_call", map[string]any{
		"session_id": sid,
		"method":     "start_mode",
		"params":     map[string]any{"speed": 3},
	})
	if !r.IsError {
		t.Fatalf("expected failure, got %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	for _, want := range []string{
		"start_mode",         // method name
		"key mode not found", // app's own error
		`{"mode":"sandbox"}`, // expected example_params
		"example_params",     // provenance of the example
		`"speed":3`,          // summary of the payload received
	} {
		if !strings.Contains(body, want) {
			t.Errorf("error missing %q; got: %s", want, body)
		}
	}
}

// 🎯T123: the payload summary stays compact — large values are reduced
// to a keys-only sketch, never dumped into the error.
func TestAppCall_FailurePayloadSummaryIsCompact(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmokeHello(t, port, appchannel.Hello{
		Methods: []appchannel.MethodDescriptor{
			{Name: appchannel.MethodPing},
			{Name: "start_mode", ExampleParams: map[string]any{"mode": "sandbox"}},
		},
	})
	defer client.close()
	sid := waitForAppSession(t, h)

	blob := strings.Repeat("x", 5000)
	r := dispatchJSON(t, h, "app_call", map[string]any{
		"session_id": sid,
		"method":     "start_mode",
		"params":     map[string]any{"blob": blob, "note": "tiny"},
	})
	if !r.IsError {
		t.Fatalf("expected failure, got %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if strings.Contains(body, strings.Repeat("x", 300)) {
		t.Errorf("payload blob leaked into the error: %d bytes", len(body))
	}
	if !strings.Contains(body, "blob") || !strings.Contains(body, "note") {
		t.Errorf("keys-only sketch missing payload keys; got: %s", body)
	}
	if len(body) > 1000 {
		t.Errorf("error message too large (%d bytes): %s", len(body), body[:200])
	}
}

func TestAppMethodsToolsDispatchKnown(t *testing.T) {
	h := startAppChannelHandler(t)
	for _, name := range []string{"app_methods", "app_call"} {
		_, err := h.Dispatch(context.Background(), name, map[string]any{
			"method": "ping",
		})
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("dispatcher rejects %q: %v", name, err)
		}
	}
}
