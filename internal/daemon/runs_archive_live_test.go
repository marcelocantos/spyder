// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/runs"
)

// TestRunsArchive_Live is the 🎯T166 end-to-end check. It is skipped
// unless SPYDER_LIVE_RUNS_ARCHIVE_DIR names an empty scratch dir, which
// becomes SPYDER_DATA_DIR — the user's ~/.spyder is never touched.
//
// Flow: reserve (HTTP, with script/app provenance) → attach real PNG
// screenshots (SPYDER_LIVE_RUNS_ARCHIVE_PNGS, colon-separated) → release
// (HTTP, with outcome) → the daemon's startup retention pass with a tiny
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
	if items, err := os.ReadDir(dir); err != nil || len(items) != 0 {
		t.Fatalf("scratch directory must exist and be empty: %v", err)
	}
	for _, tool := range []string{"cjxl", "djxl", "zstd"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("live archive backend unavailable: %s: %v", tool, err)
		}
	}
	t.Setenv(paths.DataDirEnv, dir)
	t.Setenv("SPYDER_RUNS_MAX_AGE_DAYS", "0")
	t.Setenv("SPYDER_RUNS_MAX_SIZE_GB", "0")
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
	// Use the production daemon mux and persistence, with real codec processes.
	// Build omits device supervisors: this storage journey does not drive hardware.
	start := func() *httptest.Server {
		mux, _, _, logs, channels := Build(Config{Version: "archive-live"})
		ts := httptest.NewServer(mux)
		t.Cleanup(func() { ts.Close(); logs.Close(); channels.Close() })
		return ts
	}
	ts := start()
	call := func(tool string, args map[string]any) {
		t.Helper()
		body, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Timeout: time.Minute}
		res, err := client.Post(ts.URL+"/api/v1/"+tool, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var result struct {
			IsError bool `json:"isError"`
		}
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil || result.IsError || res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d error=%v result=%+v", tool, res.StatusCode, err, result)
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
		live, err := os.ReadFile(filepath.Join(paths.RunsBase(), run.ID, a.Name))
		if err != nil || !bytes.Equal(live, b) || a.MIMEType != "image/png" {
			t.Fatalf("live capture changed before archival: %v", err)
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
	ts.Close()
	_ = start() // production Build starts retention, including its lock and policy
	manifest := filepath.Join(paths.RunsArchiveBase(), run.ID+".manifest.json")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, err := os.Stat(manifest); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup retention did not publish a manifest")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(paths.RunsBase(), run.ID)); !os.IsNotExist(err) {
		t.Fatalf("run dir still present after archive pass: %v", err)
	}

	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var rec runs.IndexRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}

	if rec.RunID != run.ID || !rec.OriginalRemoved || !rec.Verification.OK() {
		t.Fatalf("index record = %+v", rec)
	}
	if rec.Verification.DecodedPNGs != len(pngs) || rec.Verification.PixelVerifiedPNGs != 0 || !strings.Contains(string(data), "jxl-lossy(cjxl -d 3 -e 7)") {
		t.Fatalf("archive did not use lossy codec: %s", data)
	}
	// Restore a frame from the published archive with the real decoder.
	cmd := exec.Command("zstd", "-dcq", rec.ArchivePath)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(out)
	var encoded []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(h.Name, ".jxl") && encoded == nil {
			encoded, err = io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	jxl := filepath.Join(dir, "restored.jxl")
	if err := os.WriteFile(jxl, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(dir, "restored.png")
	if out, err := exec.Command("djxl", jxl, restored).CombinedOutput(); err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
	f, err := os.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := png.Decode(f); err != nil {
		t.Fatalf("restored frame: %v", err)
	}

	t.Logf("run=%s archive=%s manifest=%s raw=%d archive=%d", rec.RunID, rec.ArchivePath, rec.ManifestPath, rec.RawBytes, rec.ArchiveBytes)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
