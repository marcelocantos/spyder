// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package clidaemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/listenaddr"
	"github.com/marcelocantos/spyder/internal/listenpreflight"
)

func TestExistingInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "listen-addr")
	if ExistingInstall(path) {
		t.Fatal("missing file should be false")
	}
	if err := listenaddr.Save(path, "127.0.0.1:3030"); err != nil {
		t.Fatal(err)
	}
	if !ExistingInstall(path) {
		t.Fatal("persisted listen-addr should be true")
	}
}

func TestAutoStartPreflightRefusesDaemonPort(t *testing.T) {
	prev := listenpreflight.Hook
	defer func() { listenpreflight.Hook = prev }()
	listenpreflight.Hook = func(port int) (bool, error) {
		if port == listenpreflight.DaemonPort {
			return true, nil
		}
		return false, nil
	}
	if err := AutoStartPreflight(); err == nil {
		t.Fatal("expected refusal when daemon port in use")
	}
}

func TestAutoStartPreflightRefusesTunnelPort(t *testing.T) {
	prev := listenpreflight.Hook
	defer func() { listenpreflight.Hook = prev }()
	listenpreflight.Hook = func(port int) (bool, error) {
		if port == 60105 {
			return true, nil
		}
		return false, nil
	}
	if err := AutoStartPreflight(); err == nil {
		t.Fatal("expected refusal when tunnel port in use")
	}
}

func TestPollWhileConnRefused_stopsOnSuccess(t *testing.T) {
	calls := 0
	err := PollWhileConnRefused(context.Background(), time.Second, 10*time.Millisecond, func() (bool, error) {
		calls++
		if calls < 3 {
			return true, errors.New("refused")
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("want nil after success: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestPollWhileConnRefused_timesOut(t *testing.T) {
	err := PollWhileConnRefused(context.Background(), 50*time.Millisecond, 10*time.Millisecond, func() (bool, error) {
		return true, errors.New("still refused")
	})
	if err == nil || err.Error() != "still refused" {
		t.Fatalf("want last refused error: %v", err)
	}
}
