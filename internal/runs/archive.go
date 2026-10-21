// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Archive format (🎯T137) — byte-compatible with the 22 Oct 2026
// one-off archiver (~/.spyder/archive/runs/archive_runs.py) so the
// existing archives and index.jsonl stay one collection:
//
//	<dir>/<run-id>.tar.zst        tar of <run-id>/…, zstd -10
//	  <run-id>/archive-files.json name, size, sha256 of every original
//	                              file + stored_as + codec + restore note
//	  <run-id>/<x>.jxl            PNGs as lossless JPEG XL (cjxl -d 0 -e 7),
//	                              decoded back and compared pixel-for-pixel
//	  <run-id>/<other>            everything else verbatim
//	<dir>/<run-id>.manifest.json  index record + files + run_manifest
//	<dir>/index.jsonl             one index record per archive (append-only)
//
// Without cjxl/djxl, PNGs are stored verbatim (method "tar.zst").
// A run's directory is removed only after its archive verifies.
// Archives are never deleted or overwritten; an archive that fails
// verification is renamed to <run-id>.failed-<ts>.tar.zst and kept.

const (
	codecJXL       = "jxl-lossless(cjxl -d 0 -e 7)"
	codecCopy      = "copy"
	methodJXL      = "jxl+tar.zst"
	methodPlain    = "tar.zst"
	formatJXL      = "tar+zstd -10; PNG stored as lossless JPEG XL (pixel-verified), archive-files.json maps names+orig sha256"
	formatPlain    = "tar+zstd -10; all files stored verbatim (cjxl unavailable), archive-files.json maps names+orig sha256"
	restoreNote    = "for PNGs: djxl <stored_as> <name> (pixel-identical, not byte-identical); others copied verbatim"
	filesJSONName  = "archive-files.json"
	indexName      = "index.jsonl"
	passLockName   = ".archive.lock"
	stagingDirName = ".staging"
)

// FileEntry is one original file inside an archive.
type FileEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	StoredAs string `json:"stored_as"`
	Codec    string `json:"codec"`
}

// Verification records every check run before an original is removed.
type Verification struct {
	ZstdTest          bool   `json:"zstd_test"`
	TarListOK         bool   `json:"tar_list_ok"`
	FileCountOriginal int    `json:"file_count_original"`
	FileCountArchive  int    `json:"file_count_archive"`
	FileCountMatch    bool   `json:"file_count_match"`
	SHA256Match       bool   `json:"sha256_match"`
	EntriesMatch      bool   `json:"entries_sha256_match"`
	PixelVerifiedPNGs int    `json:"pixel_verified_pngs"`
	OriginalsSame     bool   `json:"originals_unchanged"`
	Error             string `json:"error,omitempty"`
}

// OK reports whether every check passed.
func (v Verification) OK() bool {
	return v.ZstdTest && v.TarListOK && v.FileCountMatch && v.SHA256Match &&
		v.EntriesMatch && v.OriginalsSame && v.Error == ""
}

// IndexRecord is one line of index.jsonl. Field names and order match
// the prototype; archive_method and archiver are additions.
type IndexRecord struct {
	RunID              string       `json:"run_id"`
	StartedAt          string       `json:"started_at"`
	EndedAt            string       `json:"ended_at"`
	Device             string       `json:"device"`
	Owner              string       `json:"owner"`
	Note               string       `json:"note"`
	Script             string       `json:"script"`
	Passed             string       `json:"passed"`
	Outcome            string       `json:"outcome"`
	FailureScreenshots NameList     `json:"failure_screenshots"`
	Versions           Versions     `json:"versions"`
	VersionsSource     string       `json:"versions_source"`
	FileCount          int          `json:"file_count"`
	ScreenshotCount    int          `json:"screenshot_count"`
	RawBytes           int64        `json:"raw_bytes"`
	ArchiveBytes       int64        `json:"archive_bytes"`
	ArchiveSHA256      string       `json:"archive_sha256"`
	ArchivePath        string       `json:"archive_path"`
	ArchiveFormat      string       `json:"archive_format"`
	ArchiveMethod      string       `json:"archive_method"`
	Archiver           string       `json:"archiver"`
	ArchivedAt         string       `json:"archived_at"`
	Verification       Verification `json:"verification"`
	OriginalRemoved    bool         `json:"original_removed"`
	ManifestPath       string       `json:"manifest_path"`
	Seconds            float64      `json:"seconds"`
}

// ArchiverOption configures an Archiver.
type ArchiverOption func(*Archiver)

// WithTools overrides the cjxl/djxl/zstd binaries. An empty cjxl or
// djxl selects the plain tar.zst method.
func WithTools(cjxl, djxl, zstd string) ArchiverOption {
	return func(a *Archiver) { a.cjxl, a.djxl, a.zstd = cjxl, djxl, zstd }
}

// WithOpenHandleCheck toggles the lsof open-handle check (default on
// when lsof is on PATH).
func WithOpenHandleCheck(on bool) ArchiverOption {
	return func(a *Archiver) {
		a.lsof = ""
		if on {
			if p, err := exec.LookPath("lsof"); err == nil {
				a.lsof = p
			}
		}
	}
}

// withRemove and withAfterWrite are test hooks.
func withRemove(f func(string) error) ArchiverOption { return func(a *Archiver) { a.remove = f } }
func withAfterWrite(f func(archivePath string)) ArchiverOption {
	return func(a *Archiver) { a.afterWrite = f }
}
func withEvent(f func(string)) ArchiverOption { return func(a *Archiver) { a.event = f } }

// Archiver turns closed run directories into verified archives.
type Archiver struct {
	dir              string
	cjxl, djxl, zstd string
	lsof             string
	workers          int
	remove           func(string) error
	afterWrite       func(string)
	event            func(string)
	indexMu          sync.Mutex
}

// NewArchiver returns an archiver writing into dir. Tools are found on
// PATH; a missing cjxl/djxl falls back to plain tar.zst.
func NewArchiver(dir string, opts ...ArchiverOption) *Archiver {
	look := func(n string) string {
		p, err := exec.LookPath(n)
		if err != nil {
			return ""
		}
		return p
	}
	a := &Archiver{
		dir: dir, cjxl: look("cjxl"), djxl: look("djxl"), zstd: look("zstd"),
		lsof: look("lsof"), workers: 4, remove: os.RemoveAll,
		afterWrite: func(string) {}, event: func(string) {},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Dir returns the archive directory.
func (a *Archiver) Dir() string { return a.dir }

// Method reports the archive method this archiver will use.
func (a *Archiver) Method() string {
	if a.cjxl != "" && a.djxl != "" {
		return methodJXL
	}
	return methodPlain
}

// lockPass takes an exclusive, non-blocking flock on the archive dir so
// two daemons sharing ~/.spyder never archive concurrently.
func (a *Archiver) lockPass() (func(), error) {
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(a.dir, passLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another archive pass holds %s", f.Name())
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// errSkip marks a run that was not attempted (nothing written).
type errSkip struct{ reason string }

func (e errSkip) Error() string { return e.reason }

// IsSkip reports whether err from ArchiveRun means "not attempted".
func IsSkip(err error) bool { var s errSkip; return errors.As(err, &s) }

type fileSnap struct {
	size  int64
	mtime int64
	mode  fs.FileMode
}

func snapshotDir(dir string) (map[string]fileSnap, error) {
	out := map[string]fileSnap{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = fileSnap{info.Size(), info.ModTime().UnixNano(), info.Mode()}
		return nil
	})
	return out, err
}

func sameSnap(a, b map[string]fileSnap) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (a *Archiver) inUse(dir string) bool {
	if a.lsof == "" {
		return false
	}
	// lsof matches real paths (/var → /private/var on macOS).
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	out, _ := exec.Command(a.lsof, "+D", dir).Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return len(lines) > 1
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ArchiveRun archives one closed run directory. It returns:
//   - (nil, errSkip) when the run was not attempted (open, in use,
//     archive already exists, write in progress, …);
//   - (rec, err) when an archive was written but failed verification —
//     the original is untouched and the archive is kept, renamed;
//   - (rec, nil) on success, with rec.OriginalRemoved reporting removal.
func (a *Archiver) ArchiveRun(runDir string) (*IndexRecord, error) {
	t0 := time.Now()
	rid := filepath.Base(runDir)
	rawManifest, err := os.ReadFile(filepath.Join(runDir, manifestName))
	if err != nil {
		return nil, errSkip{"no/unreadable manifest"}
	}
	var m Run
	if err := json.Unmarshal(rawManifest, &m); err != nil {
		return nil, errSkip{"no/unreadable manifest"}
	}
	if m.ClosedAt == nil {
		return nil, errSkip{"open"}
	}
	apath := filepath.Join(a.dir, rid+".tar.zst")
	if _, err := os.Stat(apath); err == nil {
		return nil, errSkip{"archive already exists"}
	}
	if a.zstd == "" {
		return nil, errSkip{"zstd not on PATH"}
	}
	if a.inUse(runDir) {
		return nil, errSkip{"in use (lsof)"}
	}
	snap, err := snapshotDir(runDir)
	if err != nil {
		return nil, errSkip{"unreadable run dir"}
	}
	var names []string
	for n, s := range snap {
		if strings.HasSuffix(n, ".tmp") {
			return nil, errSkip{"has .tmp files (write in progress?)"}
		}
		if s.mode.IsRegular() {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	stage := filepath.Join(a.dir, stagingDirName, rid)
	if err := os.RemoveAll(stage); err != nil { // our own scratch, never an archive
		return nil, errSkip{"staging cleanup failed"}
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return nil, errSkip{"staging mkdir failed"}
	}
	defer os.RemoveAll(stage)

	a.event("stage")
	entries, srcOf, storedSHA, pixelOK, err := a.stage(runDir, stage, names, snap)
	if err != nil {
		return nil, errSkip{"stage: " + err.Error()}
	}
	method := methodPlain
	format := formatPlain
	if a.Method() == methodJXL {
		method, format = methodJXL, formatJXL
	}

	filesJSON, _ := json.MarshalIndent(map[string]any{
		"run_id": rid, "files": entries, "restore": restoreNote,
	}, "", " ")

	a.event("write")
	partial := apath + ".partial"
	if err := a.writeTarZst(partial, rid, entries, srcOf, filesJSON); err != nil {
		_ = os.Remove(partial)
		return nil, errSkip{"write archive: " + err.Error()}
	}
	// No-clobber publish: link fails if an archive appeared meanwhile.
	if err := os.Link(partial, apath); err != nil {
		_ = os.Remove(partial)
		return nil, errSkip{"publish archive: " + err.Error()}
	}
	_ = os.Remove(partial)
	asha, _ := sha256File(apath)
	a.afterWrite(apath)

	a.event("verify")
	v := a.verify(apath, asha, rid, entries, storedSHA, filesJSON)
	v.PixelVerifiedPNGs = pixelOK
	v.FileCountOriginal = len(names)
	if after, err := snapshotDir(runDir); err == nil {
		v.OriginalsSame = sameSnap(snap, after)
	}
	st, _ := os.Stat(apath)

	rec := a.record(rid, &m, entries, apath, asha, st.Size(), method, format, v)
	if v.OK() && !a.inUse(runDir) {
		a.event("remove")
		if err := a.remove(runDir); err == nil {
			rec.OriginalRemoved = true
		} else {
			slog.Error("runs: archive verified but original removal failed", "run", rid, "error", err)
		}
	}
	var verr error
	if !v.OK() {
		failed := filepath.Join(a.dir, fmt.Sprintf("%s.failed-%s.tar.zst", rid, time.Now().UTC().Format("20060102T150405Z")))
		if err := os.Rename(apath, failed); err == nil {
			rec.ArchivePath = failed
			rec.ManifestPath = strings.TrimSuffix(failed, ".tar.zst") + ".manifest.json"
		}
		vj, _ := json.Marshal(v)
		verr = fmt.Errorf("verification failed: %s", vj)
	}
	rec.Seconds = float64(time.Since(t0).Milliseconds()) / 1000
	if err := a.writeManifestAndIndex(rec, entries, rawManifest); err != nil {
		slog.Error("runs: archive manifest/index write failed", "run", rid, "error", err)
	}
	return rec, verr
}

func (a *Archiver) stage(runDir, stage string, names []string, snap map[string]fileSnap) (entries []FileEntry, srcOf map[string]string, storedSHA map[string]string, pixelOK int, err error) {
	srcOf = map[string]string{}
	storedSHA = map[string]string{}
	entries = make([]FileEntry, len(names))
	useJXL := a.Method() == methodJXL
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for range a.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				n := names[i]
				src := filepath.Join(runDir, filepath.FromSlash(n))
				sum, e := sha256File(src)
				if e != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = e
					}
					mu.Unlock()
					continue
				}
				ent := FileEntry{Name: n, Size: snap[n].size, SHA256: sum, StoredAs: n, Codec: codecCopy}
				stored, storedSum := src, sum
				if useJXL && strings.EqualFold(filepath.Ext(n), ".png") {
					jxlName := n[:len(n)-4] + ".jxl"
					dst := filepath.Join(stage, filepath.FromSlash(jxlName))
					if e := a.toJXL(src, dst); e == nil {
						if s2, e := sha256File(dst); e == nil {
							ent.StoredAs, ent.Codec = jxlName, codecJXL
							stored, storedSum = dst, s2
							mu.Lock()
							pixelOK++
							mu.Unlock()
						}
					} else {
						slog.Warn("runs: jxl conversion not verified; storing PNG verbatim", "file", src, "error", e)
					}
				}
				mu.Lock()
				entries[i] = ent
				srcOf[ent.StoredAs] = stored
				storedSHA[ent.StoredAs] = storedSum
				mu.Unlock()
			}
		}()
	}
	for i := range names {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return entries, srcOf, storedSHA, pixelOK, firstErr
}

// toJXL encodes src losslessly, decodes it back with djxl and compares
// every pixel against the original. Any difference is an error.
func (a *Archiver) toJXL(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if out, err := exec.Command("nice", a.cjxl, "-d", "0", "-e", "7", "--num_threads=2", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("cjxl: %v: %s", err, bytes.TrimSpace(out))
	}
	back := dst + ".roundtrip.png"
	defer os.Remove(back)
	if out, err := exec.Command("nice", a.djxl, dst, back).CombinedOutput(); err != nil {
		return fmt.Errorf("djxl: %v: %s", err, bytes.TrimSpace(out))
	}
	same, err := samePixels(src, back)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("pixel mismatch")
	}
	return nil
}

func decodePNG(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func samePixels(a, b string) (bool, error) {
	ia, err := decodePNG(a)
	if err != nil {
		return false, err
	}
	ib, err := decodePNG(b)
	if err != nil {
		return false, err
	}
	if ia.Bounds() != ib.Bounds() {
		return false, nil
	}
	// Fast paths for identical concrete 8-bit layouts.
	switch x := ia.(type) {
	case *image.RGBA:
		if y, ok := ib.(*image.RGBA); ok && x.Stride == y.Stride {
			return bytes.Equal(x.Pix, y.Pix), nil
		}
	case *image.NRGBA:
		if y, ok := ib.(*image.NRGBA); ok && x.Stride == y.Stride {
			return bytes.Equal(x.Pix, y.Pix), nil
		}
	case *image.Gray:
		if y, ok := ib.(*image.Gray); ok && x.Stride == y.Stride {
			return bytes.Equal(x.Pix, y.Pix), nil
		}
	}
	r := ia.Bounds()
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			if color.NRGBA64Model.Convert(ia.At(px, py)) != color.NRGBA64Model.Convert(ib.At(px, py)) {
				return false, nil
			}
		}
	}
	return true, nil
}

func (a *Archiver) writeTarZst(dst, rid string, entries []FileEntry, srcOf map[string]string, filesJSON []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	cmd := exec.Command("nice", a.zstd, "-q", "-10", "-T4", "-f", "-o", dst)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	tw := tar.NewWriter(stdin)
	werr := func() error {
		now := time.Now()
		dirs := map[string]bool{}
		addDir := func(d string) error {
			if dirs[d] {
				return nil
			}
			dirs[d] = true
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o700, ModTime: now})
		}
		if err := addDir(rid); err != nil {
			return err
		}
		for _, e := range entries {
			name := rid + "/" + e.StoredAs
			for d := filepath.ToSlash(filepath.Dir(name)); d != rid && d != "."; d = filepath.ToSlash(filepath.Dir(d)) {
				if err := addDir(d); err != nil {
					return err
				}
			}
			src := srcOf[e.StoredAs]
			f, err := os.Open(src)
			if err != nil {
				return err
			}
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return err
			}
			hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}
			if err := tw.WriteHeader(hdr); err != nil {
				f.Close()
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: rid + "/" + filesJSONName, Mode: 0o600, Size: int64(len(filesJSON)), ModTime: now}); err != nil {
			return err
		}
		if _, err := tw.Write(filesJSON); err != nil {
			return err
		}
		return tw.Close()
	}()
	stdin.Close()
	cerr := cmd.Wait()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return fmt.Errorf("zstd: %v: %s", cerr, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}

func (a *Archiver) verify(apath, asha, rid string, entries []FileEntry, storedSHA map[string]string, filesJSON []byte) Verification {
	var v Verification
	v.ZstdTest = exec.Command(a.zstd, "-tq", apath).Run() == nil

	cmd := exec.Command(a.zstd, "-dcq", apath)
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	got := map[string]string{}
	if err == nil {
		tr := tar.NewReader(out)
		v.TarListOK = true
		for {
			h, terr := tr.Next()
			if terr == io.EOF {
				break
			}
			if terr != nil {
				v.TarListOK = false
				v.Error = "tar: " + terr.Error()
				break
			}
			if h.Typeflag == tar.TypeDir {
				continue
			}
			hh := sha256.New()
			if _, cerr := io.Copy(hh, tr); cerr != nil {
				v.TarListOK = false
				v.Error = "tar read: " + cerr.Error()
				break
			}
			got[h.Name] = hex.EncodeToString(hh.Sum(nil))
		}
		_, _ = io.Copy(io.Discard, out)
		if werr := cmd.Wait(); werr != nil {
			v.TarListOK = false
			if v.Error == "" {
				v.Error = "zstd -dc: " + werr.Error()
			}
		}
	} else {
		v.Error = err.Error()
	}
	v.FileCountArchive = len(got) - 1 // minus archive-files.json
	want := map[string]string{}
	for _, e := range entries {
		want[rid+"/"+e.StoredAs] = storedSHA[e.StoredAs]
	}
	fj := sha256.Sum256(filesJSON)
	want[rid+"/"+filesJSONName] = hex.EncodeToString(fj[:])
	v.FileCountMatch = len(got) == len(want) && len(entries) == v.FileCountArchive
	v.EntriesMatch = len(got) == len(want)
	for k, s := range want {
		if g, ok := got[k]; !ok {
			v.FileCountMatch = false
			v.EntriesMatch = false
		} else if g != s {
			v.EntriesMatch = false
		}
	}
	if s2, err := sha256File(apath); err == nil {
		v.SHA256Match = s2 == asha
	}
	return v
}

func (a *Archiver) record(rid string, m *Run, entries []FileEntry, apath, asha string, asize int64, method, format string, v Verification) *IndexRecord {
	vers := UnknownVersions()
	src := "run manifest.json only; spyder run manifests do not record these fields"
	if m.Versions != nil {
		vers = *m.Versions
		vers.normalize()
		src = "run manifest.json (stamped by spyder at run open/close)"
	}
	res := Result{Passed: Unknown, Outcome: Unknown}
	if m.Result != nil {
		res = *m.Result
		res.Passed, res.Outcome = orUnknown(res.Passed), orUnknown(res.Outcome)
	}
	var raw int64
	for _, e := range entries {
		raw += e.Size
	}
	shots := 0
	for _, art := range m.Artefacts {
		if art.Source == "screenshot" {
			shots++
		}
	}
	ended := Unknown
	if m.ClosedAt != nil {
		ended = m.ClosedAt.Format(time.RFC3339Nano)
	}
	started := Unknown
	if !m.CreatedAt.IsZero() {
		started = m.CreatedAt.Format(time.RFC3339Nano)
	}
	return &IndexRecord{
		RunID: rid, StartedAt: started, EndedAt: ended,
		Device: orUnknown(m.Device), Owner: orUnknown(m.Owner), Note: m.Note,
		Script: vers.Script.Path, Passed: res.Passed, Outcome: res.Outcome,
		FailureScreenshots: res.FailureScreenshots,
		Versions:           vers, VersionsSource: src,
		FileCount: len(entries), ScreenshotCount: shots,
		RawBytes: raw, ArchiveBytes: asize, ArchiveSHA256: asha, ArchivePath: apath,
		ArchiveFormat: format, ArchiveMethod: method,
		Archiver:     "spyder " + SpyderBuild().Version + " (" + SpyderBuild().Commit + ")",
		ArchivedAt:   time.Now().Format(time.RFC3339),
		Verification: v,
		ManifestPath: filepath.Join(a.dir, rid+".manifest.json"),
	}
}

func (a *Archiver) writeManifestAndIndex(rec *IndexRecord, entries []FileEntry, rawManifest []byte) error {
	recJSON, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	var full map[string]any
	if err := json.Unmarshal(recJSON, &full); err != nil {
		return err
	}
	full["files"] = entries
	full["run_manifest"] = json.RawMessage(rawManifest)
	mj, err := json.MarshalIndent(full, "", " ")
	if err != nil {
		return err
	}
	if err := atomicWrite(rec.ManifestPath, mj, 0o600); err != nil {
		return err
	}
	a.indexMu.Lock()
	defer a.indexMu.Unlock()
	f, err := os.OpenFile(filepath.Join(a.dir, indexName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(recJSON, '\n'))
	return err
}
