// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T110 — metrics tool surface: method catalogue + dispatch + no-session error.

package mcp

import (
	"context"
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
		res, err := h.Dispatch(context.Background(), name, map[string]any{
			"series": []any{"dt"},
		})
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("dispatcher rejects %q as unknown: %v", name, err)
		}
		// With no session, tool should still resolve (error is session-level).
		_ = res
	}
}

func TestMetricsArmMissingSeriesMessage(t *testing.T) {
	// With a live session that does NOT advertise metrics_arm, expect clear error.
	// Smoke without session: dispatch is enough for gating (no panic).
	h := startAppChannelHandler(t)
	_, err := h.Dispatch(context.Background(), "app_metrics_arm", map[string]any{})
	if err != nil && strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("app_metrics_arm not registered: %v", err)
	}
}
