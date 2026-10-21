// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/runs"
)

// TestRunsArchive_Live is the 🎯T166 end-to-end check. It is skipped
// unless SPYDER_LIVE_RUNS_ARCHIVE_DIR names an empty scratch dir, which
// becomes SPYDER_DATA_DIR — the user's ~/.spyder is never touched.
//
// Flow: reserve (MCP, with script/app provenance) → attach real PNG
// screenshots (SPYDER_LIVE_RUNS_ARCHIVE_PNGS, colon-separated) → release
// (MCP, with outcome) → the daemon's startup retention pass with a tiny
// size cap and no quiet period → run dir archived, verified, removed.
//
// Optional: SPYDER_LIVE_RUNS_ARCHIVE_DEVICE (default "e2e-sim", a
// simulated device: OS/app probes then record "unknown"),
// SPYDER_LIVE_RUNS_ARCHIVE_SCRIPT, SPYDER_LIVE_RUNS_ARCHIVE_BUNDLE.
func TestRunsArchive_Live(t *testing.T) {
	dir := os.Getenv("SPYDER_LIVE_RUNS_ARCHIVE_DIR")
	if dir == "" {
		t.Skip("SPYDER_LIVE_RUNS_ARCHIVE_DIR not set")
	}
	t.Setenv(paths.DataDirEnv, dir)
	devRef := envOr("SPYDER_LIVE_RUNS_ARCHIVE_DEVICE", "e2e-sim")
	script := envOr("SPYDER_LIVE_RUNS_ARCHIVE_SCRIPT", "")
	var pngs []string
	if v := os.Getenv("SPYDER_LIVE_RUNS_ARCHIVE_PNGS"); v != "" {
		pngs = strings.Split(v, ":")
	}
	if len(pngs) == 0 {
		t.Fatal("SPYDER_LIVE_RUNS_ARCHIVE_PNGS must list real PNG files")
	}

	store, err := runs.New(paths.RunsBase())
	if err != nil {
		t.Fatal(err)
	}
	resv, err := reservations.New(filepath.Join(paths.Base(), "reservations.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := spydermcp.NewHandler(spydermcp.WithRuns(store), spydermcp.WithReservations(resv))
	ctx := context.Background()
	call := func(tool string, args map[string]any) {
		t.Helper()
		res, err := h.Dispatch(ctx, tool, args)
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", tool, err, res)
		}
	}

	owner := "runs-archive-e2e"
	call("reserve", map[string]any{
		"device": devRef, "owner": owner, "note": "🎯T166 e2e",
		"script_path":   script,
		"app_bundle_id": envOr("SPYDER_LIVE_RUNS_ARCHIVE_BUNDLE", "com.marcelocantos.stockcars"),
		"app_name":      "Stock Car Racing",
		"app_commit":    "e2e0000000000000000000000000000000000000",
		"app_branch":    "e2e-branch",
		"app_dirty":     false,
	})
	run, err := store.Active(devRef, owner)
	if err != nil || run == nil {
		t.Fatalf("no active run: %v", err)
	}
	var shots []string
	for _, p := range pngs {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		a, err := store.AddArtefact(run.ID, "screenshot", filepath.Base(p), "image/png", b)
		if err != nil {
			t.Fatal(err)
		}
		shots = append(shots, a.Name)
	}
	call("release", map[string]any{
		"device": devRef, "owner": owner,
		"passed": false, "outcome": "e2e: simulated failure on last screenshot",
		"failure_screenshots": []any{shots[len(shots)-1]},
	})

	t.Setenv("SPYDER_RUNS_MAX_AGE_DAYS", "0")
	t.Setenv("SPYDER_RUNS_MAX_SIZE_GB", "0.000001") // ~1 KiB: forces the size pass
	t.Setenv("SPYDER_RUNS_ARCHIVE_QUIET_MINUTES", "0")
	policy := runsPolicyFromEnv()
	archiver := runs.NewArchiver(paths.RunsArchiveBase())
	pstore, err := runs.New(paths.RunsBase(), runs.WithPolicy(policy), runs.WithArchiver(archiver))
	if err != nil {
		t.Fatal(err)
	}
	pruneRunsOnStartup(pstore, archiver, policy)

	if _, err := os.Stat(filepath.Join(paths.RunsBase(), run.ID)); !os.IsNotExist(err) {
		t.Fatalf("run dir still present after archive pass: %v", err)
	}
	f, err := os.Open(filepath.Join(paths.RunsArchiveBase(), "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<24)
	var rec runs.IndexRecord
	for sc.Scan() {
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
	}
	if rec.RunID != run.ID || !rec.OriginalRemoved || !rec.Verification.OK() {
		t.Fatalf("index record = %+v", rec)
	}
	t.Logf("run=%s archive=%s manifest=%s raw=%d archive=%d", rec.RunID, rec.ArchivePath, rec.ManifestPath, rec.RawBytes, rec.ArchiveBytes)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
