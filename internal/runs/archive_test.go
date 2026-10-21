// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func needTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not on PATH", name)
	}
	return p
}

// testArchiver archives into a temp dir with the lsof check off (fast,
// deterministic); tests that exercise lsof turn it back on.
func testArchiver(t *testing.T, opts ...ArchiverOption) *Archiver {
	t.Helper()
	needTool(t, "zstd")
	all := append([]ArchiverOption{WithOpenHandleCheck(false)}, opts...)
	return NewArchiver(t.TempDir(), all...)
}

func plainArchiver(t *testing.T, opts ...ArchiverOption) *Archiver {
	t.Helper()
	z := needTool(t, "zstd")
	return testArchiver(t, append([]ArchiverOption{WithTools("", "", z)}, opts...)...)
}

func pngBytes(t *testing.T, alpha bool, seed int) []byte {
	t.Helper()
	const w, h = 97, 61
	var img image.Image
	if alpha {
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				m.SetNRGBA(x, y, color.NRGBA{uint8(x*3 + seed), uint8(y * 5), uint8(x ^ y), uint8(255 - x)})
			}
		}
		img = m
	} else {
		m := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				m.SetRGBA(x, y, color.RGBA{uint8(x + seed), uint8(y * 7), uint8(x * y), 255})
			}
		}
		img = m
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// closedRun creates a closed run with two PNG screenshots and a log.
func closedRun(t *testing.T, s *Store) *Run {
	t.Helper()
	r, err := s.OpenWith(OpenParams{
		Device: "Pixel", Owner: "stock-car", Note: "smoke",
		App:      AppVersion{Name: "Stock Car Racing", BundleID: "com.example.scr", Version: "2.4.1", Build: "241", Commit: "abc123", Branch: "main", Dirty: "false"},
		DeviceOS: DeviceOSVersion{Version: "14", Build: "UP1A.231005.007"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, alpha := range []bool{false, true} {
		name := []string{"shot-1.png", "fail-2.png"}[i]
		if _, err := s.AddArtefact(r.ID, "screenshot", name, "image/png", pngBytes(t, alpha, i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddArtefact(r.ID, "logs", "device.log", "text/plain", []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWith(r.ID, CloseParams{Passed: "false", Outcome: "assert failed: lap timer", FailureScreenshots: KnownNames([]string{"fail-2.png"})}); err != nil {
		t.Fatal(err)
	}
	return r
}

func readIndex(t *testing.T, dir string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, indexName))
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("index line: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func dirFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			out[rel] = b
		}
		return nil
	})
	return out
}

func TestArchive_VerificationFailureLeavesOriginalIntact(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	r := closedRun(t, s)
	runDir := filepath.Join(s.BaseDir(), r.ID)
	before := dirFiles(t, runDir)

	removed := false
	a := plainArchiver(t,
		withAfterWrite(func(p string) { // corrupt the archive after it's published
			_ = os.Truncate(p, 64)
		}),
		withRemove(func(string) error { removed = true; return nil }))

	rec, err := a.ArchiveRun(runDir)
	if err == nil || rec == nil {
		t.Fatalf("want verification failure with a record; rec=%v err=%v", rec, err)
	}
	if removed || rec.OriginalRemoved {
		t.Fatal("original was removed despite failed verification")
	}
	if rec.Verification.OK() {
		t.Fatalf("verification unexpectedly OK: %+v", rec.Verification)
	}
	after := dirFiles(t, runDir)
	if len(after) != len(before) {
		t.Fatalf("run dir changed: %d → %d files", len(before), len(after))
	}
	for k, v := range before {
		if !bytes.Equal(after[k], v) {
			t.Errorf("original %s modified", k)
		}
	}
	// The failed archive is kept (renamed), never deleted.
	if !strings.Contains(rec.ArchivePath, ".failed-") {
		t.Errorf("failed archive not renamed: %s", rec.ArchivePath)
	}
	if _, err := os.Stat(rec.ArchivePath); err != nil {
		t.Errorf("failed archive missing: %v", err)
	}
	idx := readIndex(t, a.Dir())
	if len(idx) != 1 || idx[0]["original_removed"] != false {
		t.Errorf("index should record original_removed=false: %v", idx)
	}
	// A later pass can retry because <id>.tar.zst no longer exists.
	if _, err := os.Stat(filepath.Join(a.Dir(), r.ID+".tar.zst")); !os.IsNotExist(err) {
		t.Errorf("unexpected canonical archive after failure: %v", err)
	}
}

func TestArchive_NoDeletionBeforeVerification(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	r := closedRun(t, s)
	runDir := filepath.Join(s.BaseDir(), r.ID)

	var events []string
	var dirAtVerify bool
	a := plainArchiver(t, withEvent(func(e string) {
		events = append(events, e)
		if e == "verify" {
			_, err := os.Stat(runDir)
			dirAtVerify = err == nil
		}
	}))
	rec, err := a.ArchiveRun(runDir)
	if err != nil {
		t.Fatalf("ArchiveRun: %v", err)
	}
	want := []string{"stage", "write", "verify", "remove"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %v; want %v", events, want)
	}
	if !dirAtVerify {
		t.Fatal("run dir already gone when verification started")
	}
	if !rec.OriginalRemoved {
		t.Fatal("verified run not removed")
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("run dir still present: %v", err)
	}
	if _, err := os.Stat(rec.ArchivePath); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
}

func TestPrune_ClosedOnlyOpenRunsNeverArchived(t *testing.T) {
	now := time.Now().Add(-72 * time.Hour)
	a := plainArchiver(t)
	s := newTestStore(t, &now, WithPolicy(Policy{MaxAge: time.Hour, MaxSize: 1}), WithArchiver(a))
	open, _ := s.Open("A", "o", "")
	_, _ = s.AddArtefact(open.ID, "screenshot", "x.png", "image/png", pngBytes(t, false, 1))
	closed, _ := s.Open("B", "o", "")
	_ = s.Close(closed.ID)
	now = time.Now()

	res, err := s.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Removed, []string{closed.ID}) {
		t.Fatalf("removed = %v; want [%s]", res.Removed, closed.ID)
	}
	if res.Skipped["open"] == 0 {
		t.Errorf("open run not reported as skipped: %v", res.Skipped)
	}
	if _, err := os.Stat(filepath.Join(s.BaseDir(), open.ID, "x.png")); err != nil {
		t.Fatalf("open run touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Dir(), open.ID+".tar.zst")); !os.IsNotExist(err) {
		t.Fatal("open run was archived")
	}
	// Direct call refuses too.
	if _, err := a.ArchiveRun(filepath.Join(s.BaseDir(), open.ID)); !IsSkip(err) {
		t.Fatalf("ArchiveRun(open) err = %v; want skip", err)
	}
}

func TestPrune_QuietPeriodSkipsRecentRuns(t *testing.T) {
	now := time.Now().Add(-72 * time.Hour)
	a := plainArchiver(t)
	s := newTestStore(t, &now, WithPolicy(Policy{MaxAge: time.Hour, Quiet: 2 * time.Hour}), WithArchiver(a))
	r, _ := s.Open("A", "o", "")
	_ = s.Close(r.ID) // closed 72h ago by the fake clock, but files were just written
	now = time.Now()
	res, err := s.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || res.Skipped["modified recently"] != 1 {
		t.Fatalf("recently-modified run not skipped: %+v", res)
	}
}

func TestPrune_NoArchiverIsNonDestructive(t *testing.T) {
	now := time.Now().Add(-72 * time.Hour)
	s := newTestStore(t, &now, WithPolicy(Policy{MaxAge: time.Hour, MaxSize: 1}))
	r, _ := s.Open("A", "o", "")
	_ = s.Close(r.ID)
	now = time.Now()
	res, err := s.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 {
		t.Fatalf("pruned without an archiver: %v", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(s.BaseDir(), r.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestArchive_NeverOverwritesExistingArchive(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	r := closedRun(t, s)
	a := plainArchiver(t)
	existing := filepath.Join(a.Dir(), r.ID+".tar.zst")
	if err := os.WriteFile(existing, []byte("prior archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ArchiveRun(filepath.Join(s.BaseDir(), r.ID)); !IsSkip(err) {
		t.Fatalf("err = %v; want skip", err)
	}
	if b, _ := os.ReadFile(existing); string(b) != "prior archive" {
		t.Fatal("existing archive overwritten")
	}
	if _, err := os.Stat(filepath.Join(s.BaseDir(), r.ID)); err != nil {
		t.Fatal("original removed")
	}
}

func TestArchive_SkipsRunWithOpenHandle(t *testing.T) {
	needTool(t, "lsof")
	now := time.Now()
	s := newTestStore(t, &now)
	r := closedRun(t, s)
	runDir := filepath.Join(s.BaseDir(), r.ID)
	f, err := os.Open(filepath.Join(runDir, "device.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := plainArchiver(t, WithOpenHandleCheck(true))
	if _, err := a.ArchiveRun(runDir); !IsSkip(err) || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("err = %v; want in-use skip", err)
	}
}

func TestArchive_JXLManifestAndIndexRecordWritten(t *testing.T) {
	needTool(t, "cjxl")
	djxl := needTool(t, "djxl")
	now := time.Now()
	s := newTestStore(t, &now)
	r := closedRun(t, s)
	runDir := filepath.Join(s.BaseDir(), r.ID)
	origShot, _ := os.ReadFile(filepath.Join(runDir, "fail-2.png"))

	a := testArchiver(t)
	if a.Method() != methodJXL {
		t.Fatalf("method = %s", a.Method())
	}
	rec, err := a.ArchiveRun(runDir)
	if err != nil {
		t.Fatalf("ArchiveRun: %v", err)
	}
	v := rec.Verification
	if !v.OK() || v.PixelVerifiedPNGs != 2 || v.FileCountOriginal != 4 || v.FileCountArchive != 4 {
		t.Fatalf("verification = %+v", v)
	}
	if !rec.OriginalRemoved {
		t.Fatal("original not removed after verification")
	}

	idx := readIndex(t, a.Dir())
	if len(idx) != 1 {
		t.Fatalf("index lines = %d", len(idx))
	}
	e := idx[0]
	checks := map[string]any{
		"run_id": r.ID, "device": "Pixel", "owner": "stock-car", "passed": "false",
		"outcome": "assert failed: lap timer", "archive_method": methodJXL,
		"file_count": float64(4), "screenshot_count": float64(2), "original_removed": true,
	}
	for k, want := range checks {
		if e[k] != want {
			t.Errorf("index[%s] = %v; want %v", k, e[k], want)
		}
	}
	if fs, _ := e["failure_screenshots"].([]any); len(fs) != 1 || fs[0] != "fail-2.png" {
		t.Errorf("failure_screenshots = %v", e["failure_screenshots"])
	}
	vers := e["versions"].(map[string]any)
	app := vers["app"].(map[string]any)
	if app["version"] != "2.4.1" || app["build"] != "241" || app["commit"] != "abc123" || app["dirty"] != "false" {
		t.Errorf("app versions = %v", app)
	}
	if osv := vers["device_os"].(map[string]any); osv["version"] != "14" || osv["build"] != "UP1A.231005.007" {
		t.Errorf("device_os = %v", osv)
	}
	if sc := vers["script"].(map[string]any); sc["path"] != Unknown {
		t.Errorf("script path should be unknown when not supplied: %v", sc)
	}

	mb, err := os.ReadFile(rec.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var man map[string]any
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatal(err)
	}
	if files, _ := man["files"].([]any); len(files) != 4 {
		t.Errorf("manifest files = %v", man["files"])
	}
	if rm, _ := man["run_manifest"].(map[string]any); rm["id"] != r.ID {
		t.Errorf("manifest run_manifest = %v", man["run_manifest"])
	}

	// Restore one screenshot and compare pixels with the original.
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "orig.png"), origShot, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("zstd", "-dcq", rec.ArchivePath).Output()
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(out))
	var jxl []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == r.ID+"/fail-2.jxl" {
			jxl, _ = io.ReadAll(tr)
		}
	}
	if jxl == nil {
		t.Fatal("fail-2.jxl not in archive")
	}
	_ = os.WriteFile(filepath.Join(tmp, "x.jxl"), jxl, 0o600)
	if out, err := exec.Command(djxl, filepath.Join(tmp, "x.jxl"), filepath.Join(tmp, "back.png")).CombinedOutput(); err != nil {
		t.Fatalf("djxl: %v %s", err, out)
	}
	if same, err := samePixels(filepath.Join(tmp, "orig.png"), filepath.Join(tmp, "back.png")); err != nil || !same {
		t.Fatalf("restored pixels differ: same=%v err=%v", same, err)
	}
}

func TestArchive_PlainFallbackWithoutCJXL(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	r := closedRun(t, s)
	a := plainArchiver(t)
	rec, err := a.ArchiveRun(filepath.Join(s.BaseDir(), r.ID))
	if err != nil {
		t.Fatal(err)
	}
	if rec.ArchiveMethod != methodPlain || rec.ArchiveFormat != formatPlain {
		t.Errorf("method/format = %s / %s", rec.ArchiveMethod, rec.ArchiveFormat)
	}
	if rec.Verification.PixelVerifiedPNGs != 0 || !rec.OriginalRemoved {
		t.Errorf("rec = %+v", rec)
	}
}

func TestArchive_LegacyManifestRecordsUnknown(t *testing.T) {
	// A pre-🎯T137 run: manifest without versions/result.
	base := t.TempDir()
	dir := filepath.Join(base, "20261021-000000-abcdef")
	_ = os.MkdirAll(dir, 0o700)
	closed := time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	_ = os.WriteFile(filepath.Join(dir, manifestName), []byte(`{"id":"20261021-000000-abcdef","device":"A23","owner":"euan-shop","created_at":"`+closed+`","closed_at":"`+closed+`"}`), 0o600)
	a := plainArchiver(t)
	rec, err := a.ArchiveRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rec)
	for _, want := range []string{`"passed":"unknown"`, `"outcome":"unknown"`, `"failure_screenshots":"unknown"`, `"script":"unknown"`, `"versions_source":"run manifest.json only`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("legacy record missing %s: %s", want, b)
		}
	}
	if rec.Versions != UnknownVersions() {
		t.Errorf("legacy versions not all unknown: %+v", rec.Versions)
	}
}

func TestArchive_PixelMismatchStoresPNGVerbatim(t *testing.T) {
	cjxl := needTool(t, "cjxl")
	z := needTool(t, "zstd")
	// A djxl that "decodes" to a different image: the round-trip check
	// must catch it and fall back to storing the PNG byte-for-byte.
	bin := t.TempDir()
	other := filepath.Join(bin, "other.png")
	_ = os.WriteFile(other, pngBytes(t, false, 99), 0o600)
	fake := filepath.Join(bin, "djxl")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\ncp '"+other+"' \"$2\"\n"), 0o700)

	now := time.Now()
	s := newTestStore(t, &now)
	r := closedRun(t, s)
	a := testArchiver(t, WithTools(cjxl, fake, z))
	rec, err := a.ArchiveRun(filepath.Join(s.BaseDir(), r.ID))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Verification.PixelVerifiedPNGs != 0 {
		t.Errorf("pixel_verified_pngs = %d; want 0", rec.Verification.PixelVerifiedPNGs)
	}
	mb, _ := os.ReadFile(rec.ManifestPath)
	if strings.Contains(string(mb), codecJXL) || !strings.Contains(string(mb), `"stored_as": "fail-2.png"`) {
		t.Errorf("mismatched PNG not stored verbatim: %s", mb)
	}
}
