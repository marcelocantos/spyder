// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package battery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_AppendQueryRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 26, 15, 4, 5, 0, time.UTC)
	l := 81
	chg := true
	if err := st.Append(Sample{TS: t0, DeviceID: "pad", Alias: "iPad", Platform: "ios", BatteryLevel: &l, Charging: &chg}); err != nil {
		t.Fatal(err)
	}
	l2 := 40
	if err := st.Append(Sample{TS: t0.Add(time.Minute), DeviceID: "phone", Alias: "S24", Platform: "android", BatteryLevel: &l2}); err != nil {
		t.Fatal(err)
	}
	all, err := st.Query(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("Query all = %d, want 2", len(all))
	}
	if all[0].DeviceID != "pad" || all[1].DeviceID != "phone" {
		t.Errorf("order = %s, %s", all[0].DeviceID, all[1].DeviceID)
	}
	if all[0].BatteryLevel == nil || *all[0].BatteryLevel != 81 {
		t.Errorf("level = %v", all[0].BatteryLevel)
	}

	only, err := st.Query(Query{DeviceID: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].DeviceID != "phone" {
		t.Fatalf("device filter = %+v", only)
	}

	window, err := st.Query(Query{Since: t0.Add(30 * time.Second), Until: t0.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 1 || window[0].DeviceID != "phone" {
		t.Fatalf("window = %+v", window)
	}
}

func TestStore_QuerySkipsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	day := filepath.Join(dir, "2026-09-26.jsonl")
	if err := os.WriteFile(day, []byte("not json\n{\"ts\":\"2026-09-26T12:00:00Z\",\"device_id\":\"ok\",\"battery_level\":7}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := st.Query(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestStore_PruneDropsOldDays(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	keep := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	l := 1
	if err := st.Append(Sample{TS: old, DeviceID: "a", BatteryLevel: &l}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(Sample{TS: keep, DeviceID: "b", BatteryLevel: &l}); err != nil {
		t.Fatal(err)
	}
	if err := st.Prune(keep, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := st.Query(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != "b" {
		t.Fatalf("after prune %+v", got)
	}
}

func TestOpen_EmptyDirRejected(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected error")
	}
}
