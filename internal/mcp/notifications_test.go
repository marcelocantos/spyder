// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcelocantos/spyder/internal/notify"
	"github.com/marcelocantos/spyder/internal/paths"
)

func TestNotificationsResolveOfflineInventoryAndAppExec(t *testing.T) {
	root := t.TempDir()
	t.Setenv(paths.DataDirEnv, root)
	if err := os.WriteFile(filepath.Join(root, "inventory.json"), []byte(`[{"alias":"iPad","platform":"ios","ios_uuid":"00008103-001122334455667A","ios_coredevice":"12345678-1234-1234-1234-123456789abc"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := notify.Open(filepath.Join(root, "notifications.json"), func(ctx context.Context, _, _ string) error { <-ctx.Done(); return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(&stubAdapter{}, &stubAdapter{})
	h.SetNotifications(m)
	res, err := h.Dispatch(context.Background(), "app_exec", map[string]any{"script": `emit(notify_when_connected(device="iPad", webhook_url=params["webhook_url"]))`, "params": map[string]any{"webhook_url": "https://example.com/private"}})
	if err != nil || res.IsError {
		t.Fatalf("app_exec notification: %v %+v", err, res)
	}
	rows := m.List()
	if len(rows) != 1 || rows[0].DeviceID != "00008103-001122334455667A" || rows[0].State != "waiting" {
		t.Fatalf("offline identity: %+v", rows)
	}
	listed, err := h.Dispatch(context.Background(), "notifications", nil)
	if err != nil || listed.IsError {
		t.Fatalf("list: %v %+v", err, listed)
	}
	cancelled, err := h.Dispatch(context.Background(), "notification_cancel", map[string]any{"id": rows[0].ID})
	if err != nil || cancelled.IsError || m.List()[0].State != "cancelled" {
		t.Fatalf("cancel: %v %+v", err, cancelled)
	}
	for _, args := range []map[string]any{
		{"device": "typo", "webhook_url": "https://example.com/hook"},
		{"device": "iPad", "platform": "android", "webhook_url": "https://example.com/hook"},
		{"device": "12345678-1234-1234-1234-123456789abc", "platform": "ios", "webhook_url": "https://example.com/hook", "ttl": "bad"},
		{"device": "unknown", "platform": "desktop", "webhook_url": "https://example.com/hook"},
		{"device": "iPad", "ttl": "0s", "webhook_url": "https://example.com/hook"},
	} {
		r, err := h.Dispatch(context.Background(), "notify_when_connected", args)
		if err == nil && !r.IsError {
			t.Fatalf("invalid registration accepted: %+v", args)
		}
	}
}
