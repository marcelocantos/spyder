// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"context"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func TestReconcile_SingleBouncePerEpisode(t *testing.T) {
	wifi := Status{Device: "en0", Connected: true, SSID: "Home"}
	stale := appchannel.ConnectivityReport{PingFailures: 1}
	healthy := appchannel.ConnectivityReport{}
	report := stale
	var confirms, bounces int

	deps := Deps{
		ReadStatus: func() (Status, error) { return wifi, nil },
		AppChannel: func() appchannel.ConnectivityReport { return report },
		UsbWedged:  func() (bool, error) { return false, nil },
		Confirm: func(_ context.Context, _ string) (bool, error) {
			confirms++
			return true, nil
		},
		Bounce: func(_ context.Context, _ string) error {
			bounces++
			report = healthy
			return nil
		},
	}

	ctx := context.Background()
	var st episodeState

	reconcile(ctx, deps, &st, "t1")
	if confirms != 0 || bounces != 0 {
		t.Fatalf("cycle1 confirms=%d bounces=%d; want 0/0", confirms, bounces)
	}

	reconcile(ctx, deps, &st, "t2")
	if confirms != 1 || bounces != 1 {
		t.Fatalf("cycle2 confirms=%d bounces=%d; want 1/1", confirms, bounces)
	}

	reconcile(ctx, deps, &st, "t3")
	if confirms != 1 || bounces != 1 {
		t.Fatalf("cycle3 confirms=%d bounces=%d; want still 1/1", confirms, bounces)
	}
}

func TestReconcile_UserDecline(t *testing.T) {
	deps := Deps{
		ReadStatus: func() (Status, error) {
			return Status{Device: "en0", Connected: true, SSID: "X"}, nil
		},
		AppChannel: func() appchannel.ConnectivityReport {
			return appchannel.ConnectivityReport{MissingDialBacks: 1}
		},
		UsbWedged: func() (bool, error) { return false, nil },
		Confirm:   func(_ context.Context, _ string) (bool, error) { return false, nil },
		Bounce: func(_ context.Context, _ string) error {
			t.Fatal("bounce should not run when user declines")
			return nil
		},
	}

	var st episodeState
	reconcile(context.Background(), deps, &st, "warmup")
	reconcile(context.Background(), deps, &st, "decline")
	if !st.declined || st.attempted {
		t.Fatalf("declined=%v attempted=%v", st.declined, st.attempted)
	}
}

func TestRunMonitor_ExitsOnContextCancel(t *testing.T) {
	save := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = save })

	deps := Deps{
		ReadStatus: func() (Status, error) { return Status{}, errNoWifi{} },
		AppChannel: func() appchannel.ConnectivityReport { return appchannel.ConnectivityReport{} },
		UsbWedged:  func() (bool, error) { return false, nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunMonitor(ctx, deps)
		close(done)
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunMonitor did not exit on cancel")
	}
}

type errNoWifi struct{}

func (errNoWifi) Error() string { return "no Wi-Fi" }
