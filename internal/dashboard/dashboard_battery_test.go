// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package dashboard_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/battery"
	"github.com/marcelocantos/spyder/internal/dashboard"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/rest"
)

func TestDashboard_BatteryTabMarkup(t *testing.T) {
	srv := httptest.NewServer(dashboard.NewHandler())
	t.Cleanup(srv.Close)
	body := httpGet(t, srv.URL+"/")
	for _, want := range []string{
		`data-tab="battery"`,
		`id="tab-battery"`,
		`id="batt-chart"`,
		`id="batt-cards"`,
		`id="batt-legend"`,
		`batt-dot`,
		`#battery`,
		`battery_history`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("dashboard HTML missing %q", want)
		}
	}
}

func TestDashboard_BatteryHistoryDataPath(t *testing.T) {
	st, err := battery.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Add(-3 * time.Hour)
	lPad, lPhone := 88, 19
	chg := true
	if err := st.Append(battery.Sample{TS: t0, DeviceID: "pad", Alias: "iPad", Platform: "ios", BatteryLevel: &lPad, Charging: &chg}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(battery.Sample{TS: t0.Add(time.Hour), DeviceID: "pad", Alias: "iPad", Platform: "ios", BatteryLevel: &lPhone}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(battery.Sample{TS: t0, DeviceID: "phone", Alias: "S24", Platform: "android", BatteryLevel: &lPhone}); err != nil {
		t.Fatal(err)
	}

	h := spydermcp.NewHandler()
	h.SetBatteryStore(st)
	mux := http.NewServeMux()
	mux.Handle(rest.Prefix, rest.NewHandler(h))
	mux.Handle(dashboard.Path, dashboard.NewHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	page := httpGet(t, srv.URL+dashboard.Path)
	if !bytes.Contains(page, []byte("spyder dashboard")) {
		t.Fatal("dashboard page missing")
	}

	res := postTool(t, srv.URL, "battery_history", map[string]any{"since": "-6h"})
	samples, _ := res["samples"].([]any)
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 3; body=%v", len(samples), res)
	}
	latest, _ := res["latest"].([]any)
	if len(latest) != 2 {
		t.Fatalf("latest = %d, want 2", len(latest))
	}
}

func TestDashboard_BatteryHistoryJSONRoundTrip(t *testing.T) {
	// Guards the shape the dashboard JS reads: samples[].battery_level and latest[].alias.
	st, err := battery.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := 42
	if err := st.Append(battery.Sample{
		TS:           time.Now().UTC().Add(-time.Minute),
		DeviceID:     "u",
		Alias:        "iPad",
		Platform:     "ios",
		BatteryLevel: &l,
	}); err != nil {
		t.Fatal(err)
	}
	h := spydermcp.NewHandler()
	h.SetBatteryStore(st)
	mux := http.NewServeMux()
	mux.Handle(rest.Prefix, rest.NewHandler(h))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res := postTool(t, srv.URL, "battery_history", map[string]any{"since": "-1h"})
	raw, _ := json.Marshal(res)
	if !bytes.Contains(raw, []byte(`"battery_level"`)) || !bytes.Contains(raw, []byte(`"iPad"`)) {
		t.Fatalf("dashboard would see empty series: %s", raw)
	}
}
