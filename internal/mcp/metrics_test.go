// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T110 — metrics tool surface: catalogue, session targeting, series
// selection, and dump shape (full retained-frame history, not gauges).

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func TestMetricsMethodsInKnownCatalogue(t *testing.T) {
	want := []string{
		appchannel.MethodMetricsList,
		appchannel.MethodMetricsArm,
		appchannel.MethodMetricsDisarm,
		appchannel.MethodMetricsStatus,
		appchannel.MethodMetricsDump,
	}
	have := map[string]bool{}
	for _, m := range appchannel.KnownMethods {
		have[m] = true
	}
	for _, m := range want {
		if !have[m] {
			t.Errorf("KnownMethods missing %q", m)
		}
	}
}

func TestMetricsToolsDispatchKnown(t *testing.T) {
	h := startAppChannelHandler(t)
	for _, name := range []string{
		"app_metrics_list", "app_metrics_arm", "app_metrics_disarm",
		"app_metrics_status", "app_metrics_dump",
	} {
		_, err := h.Dispatch(context.Background(), name, map[string]any{
			"series": []any{"dt"},
		})
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("dispatcher rejects %q as unknown: %v", name, err)
		}
	}
}

func TestMetricsNoSessionClearError(t *testing.T) {
	h := startAppChannelHandler(t)
	r := dispatchJSON(t, h, "app_metrics_list", map[string]any{
		"session_id": "no-such-session",
	})
	if !r.IsError {
		t.Fatal("expected error with missing session")
	}
	body := resultText(t, &r)
	// requireSession wording — session not found / no session.
	if !strings.Contains(strings.ToLower(body), "session") {
		t.Errorf("error should mention session: %s", body)
	}
}

func TestMetricsUnsupportedAppClearError(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	// Advertise only ping — metrics_* must fail closed with clear message.
	client := dialSmoke(t, port, []string{appchannel.MethodPing})
	defer client.close()
	sid := waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_metrics_list", map[string]any{"session_id": sid})
	if !r.IsError {
		t.Fatalf("expected unsupported error, got: %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "does not advertise metrics_list") {
		t.Errorf("want clear unsupported message, got: %s", body)
	}
}

func TestMetricsArmRequiresSeries(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmoke(t, port, []string{
		appchannel.MethodMetricsList,
		appchannel.MethodMetricsArm,
		appchannel.MethodMetricsDisarm,
		appchannel.MethodMetricsStatus,
		appchannel.MethodMetricsDump,
	})
	defer client.close()
	sid := waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_metrics_arm", map[string]any{"session_id": sid})
	if !r.IsError {
		t.Fatalf("expected series required error, got: %s", resultText(t, &r))
	}
	if !strings.Contains(resultText(t, &r), "series") {
		t.Errorf("error should mention series: %s", resultText(t, &r))
	}
}

// TestMetrics_RPCShapes exercises the full list→arm→status→dump→disarm
// path against a smoke app that mirrors ge 🎯T166 JSON shapes (instance,
// series names, capacity, full frames history).
func TestMetrics_RPCShapes(t *testing.T) {
	h := startAppChannelHandler(t)
	_, port := openListener(t, h)
	client := dialSmoke(t, port, []string{
		appchannel.MethodMetricsList,
		appchannel.MethodMetricsArm,
		appchannel.MethodMetricsDisarm,
		appchannel.MethodMetricsStatus,
		appchannel.MethodMetricsDump,
	})
	defer client.close()
	sid := waitForAppSession(t, h)

	// list — registered series catalogue for the instance
	listBody := dispatchJSONMap(t, h, "app_metrics_list", map[string]any{
		"session_id": sid,
		"instance":   "game0",
	})
	listRes, ok := listBody["result"].(map[string]any)
	if !ok {
		t.Fatalf("list result not object: %#v", listBody)
	}
	if listRes["instance"] != "game0" {
		t.Errorf("list instance: got %v want game0", listRes["instance"])
	}
	series, ok := listRes["series"].([]any)
	if !ok || len(series) < 2 {
		t.Fatalf("list series: %#v", listRes["series"])
	}
	names := map[string]bool{}
	for _, el := range series {
		m, _ := el.(map[string]any)
		if n, ok := m["name"].(string); ok {
			names[n] = true
		}
	}
	if !names["dt"] || !names["zoom"] {
		t.Errorf("list missing dt/zoom: %v", names)
	}
	if client.lastMetricsParams["instance"] != "game0" {
		t.Errorf("list RPC did not forward instance: %#v", client.lastMetricsParams)
	}

	// arm — series selection + capacity for that instance only
	armBody := dispatchJSONMap(t, h, "app_metrics_arm", map[string]any{
		"session_id": sid,
		"instance":   "game0",
		"series":     []any{"dt", "zoom"},
		"capacity":   64.0, // MCP JSON numbers are float64
	})
	armRes, ok := armBody["result"].(map[string]any)
	if !ok {
		t.Fatalf("arm result not object: %#v", armBody)
	}
	if armRes["armed"] != true {
		t.Errorf("arm armed: %#v", armRes["armed"])
	}
	if armRes["instance"] != "game0" {
		t.Errorf("arm instance: %#v", armRes["instance"])
	}
	// capacity may decode as float64 or int64 depending on msgpack path
	if !approxInt(armRes["capacity"], 64) {
		t.Errorf("arm capacity: %#v want 64", armRes["capacity"])
	}
	armedSeries := asStringSlice(armRes["series"])
	if len(armedSeries) != 2 || armedSeries[0] != "dt" || armedSeries[1] != "zoom" {
		t.Errorf("arm series: %#v", armRes["series"])
	}

	// status — same instance targeting
	stBody := dispatchJSONMap(t, h, "app_metrics_status", map[string]any{
		"session_id": sid,
		"instance":   "game0",
	})
	stRes, ok := stBody["result"].(map[string]any)
	if !ok {
		t.Fatalf("status result: %#v", stBody)
	}
	if stRes["armed"] != true {
		t.Errorf("status armed: %#v", stRes["armed"])
	}
	if !approxInt(stRes["count"], 2) {
		t.Errorf("status count (fixture retained frames): %#v", stRes["count"])
	}

	// dump — full retained-frame history (not latest-only gauges)
	dumpBody := dispatchJSONMap(t, h, "app_metrics_dump", map[string]any{
		"session_id": sid,
		"instance":   "game0",
	})
	dumpRes, ok := dumpBody["result"].(map[string]any)
	if !ok {
		t.Fatalf("dump result: %#v", dumpBody)
	}
	frames, ok := dumpRes["frames"].([]any)
	if !ok || len(frames) != 2 {
		t.Fatalf("dump frames (want 2 retained): %#v", dumpRes["frames"])
	}
	row0, ok := frames[0].([]any)
	if !ok || len(row0) != 2 {
		t.Fatalf("dump frame0 columns: %#v", frames[0])
	}
	if !approxInt(dumpRes["count"], 2) {
		t.Errorf("dump count: %#v", dumpRes["count"])
	}
	dumpSeries := asStringSlice(dumpRes["series"])
	if len(dumpSeries) != 2 {
		t.Errorf("dump series: %#v", dumpRes["series"])
	}

	// disarm
	disBody := dispatchJSONMap(t, h, "app_metrics_disarm", map[string]any{
		"session_id": sid,
		"instance":   "game0",
	})
	disRes, ok := disBody["result"].(map[string]any)
	if !ok {
		t.Fatalf("disarm result: %#v", disBody)
	}
	if disRes["armed"] != false {
		t.Errorf("disarm armed: %#v", disRes["armed"])
	}
}

func dispatchJSONMap(t *testing.T, h *Handler, name string, args map[string]any) map[string]any {
	t.Helper()
	r := dispatchJSON(t, h, name, args)
	if r.IsError {
		t.Fatalf("%s error: %s", name, resultText(t, &r))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(resultText(t, &r)), &body); err != nil {
		t.Fatalf("%s unmarshal: %v body=%s", name, err, resultText(t, &r))
	}
	return body
}

func asStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		if s, ok := el.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func approxInt(v any, want int) bool {
	switch n := v.(type) {
	case int:
		return n == want
	case int64:
		return int(n) == want
	case float64:
		return int(n) == want
	case json.Number:
		i, err := n.Int64()
		return err == nil && int(i) == want
	default:
		return false
	}
}
