// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func TestAppCallsBatch_InvokeInOrder(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmokeHello(t, port, appchannel.Hello{
		AppName: "batch-smoke",
		Methods: []appchannel.MethodDescriptor{
			{Name: "echo"},
		},
	})
	defer client.close()
	sid := waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_calls_batch", map[string]any{
		"session_id": sid,
		"calls": []any{
			map[string]any{"method": "echo", "params": map[string]any{"n": 1}},
			map[string]any{"method": "echo", "params": map[string]any{"n": 2}},
		},
	})
	if r.IsError {
		t.Fatalf("app_calls_batch: %s", resultText(t, &r))
	}
	body := dispatchJSONMap(t, h, "app_calls_batch", map[string]any{
		"session_id": sid,
		"calls": []any{
			map[string]any{"method": "echo", "params": map[string]any{"n": 1}},
			map[string]any{"method": "echo", "params": map[string]any{"n": 2}},
		},
	})
	results, ok := body["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results=%v", body["results"])
	}
}

func TestExec_FailBuiltin(t *testing.T) {
	res := runScript(t, `fail("nope")`, stubVerbs(), defaultLim())
	if !res.IsError {
		t.Fatal("expected IsError")
	}
	joined := strings.Join(texts(res), "\n")
	if !strings.Contains(joined, "fail: nope") {
		t.Fatalf("texts=%v", texts(res))
	}
}
