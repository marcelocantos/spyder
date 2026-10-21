// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// Unknown is written for every provenance field spyder could not
// observe directly. Fields are never inferred (e.g. from timestamps).
const Unknown = "unknown"

// Build stamp, settable via -ldflags "-X github.com/marcelocantos/spyder/internal/runs.BuildBranch=...".
// Commit and dirty fall back to the Go toolchain's vcs stamp
// (debug.ReadBuildInfo); branch is only known through ldflags because
// the toolchain does not record it.
var (
	BuildVersion string
	BuildCommit  string
	BuildBranch  string
	BuildDirty   string
)

// Versions records the source versions behind one run (🎯T137).
// Every leaf is a string so "unknown" is representable uniformly.
type Versions struct {
	App      AppVersion      `json:"app"`
	Spyder   SpyderVersion   `json:"spyder"`
	Script   ScriptVersion   `json:"script"`
	DeviceOS DeviceOSVersion `json:"device_os"`
}

// AppVersion describes the app under test. Name/BundleID/Version/Build
// come from the installed package where the device adapter can read
// it; Commit/Branch/Dirty are supplied by the caller (spyder cannot
// see the app's source tree).
type AppVersion struct {
	Name     string `json:"name"`
	BundleID string `json:"bundle_id"`
	Version  string `json:"version"`
	Build    string `json:"build"`
	Commit   string `json:"commit"`
	Branch   string `json:"branch"`
	Dirty    string `json:"dirty"`
}

// SpyderVersion is the build stamp of the spyder binary that opened the run.
type SpyderVersion struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Branch  string `json:"branch"`
	Dirty   string `json:"dirty"`
}

// ScriptVersion describes the .star script driving the run. Commit,
// Branch and Dirty describe the git repo containing the script, read
// when the run opens; FileDirty reports whether the script file itself
// differs from HEAD.
type ScriptVersion struct {
	Path      string `json:"path"`
	Source    string `json:"source"`
	Commit    string `json:"commit"`
	Branch    string `json:"branch"`
	Dirty     string `json:"dirty"`
	FileDirty string `json:"file_dirty"`
}

// DeviceOSVersion is the OS of the device the run was reserved on.
type DeviceOSVersion struct {
	Version string `json:"version"`
	Build   string `json:"build"`
}

// Result is stamped when the run closes.
type Result struct {
	Passed             string   `json:"passed"`  // "true", "false" or "unknown"
	Outcome            string   `json:"outcome"` // free text from the caller, or "unknown"
	FailureScreenshots NameList `json:"failure_screenshots"`
}

// NameList is a list of artefact names that may itself be unknown. It
// marshals as "unknown" when Known is false, else as a JSON array.
type NameList struct {
	Known bool
	Names []string
}

// KnownNames returns a known NameList (nil names become empty).
func KnownNames(names []string) NameList {
	if names == nil {
		names = []string{}
	}
	return NameList{Known: true, Names: names}
}

func (n NameList) MarshalJSON() ([]byte, error) {
	if !n.Known {
		return json.Marshal(Unknown)
	}
	names := n.Names
	if names == nil {
		names = []string{}
	}
	return json.Marshal(names)
}

func (n *NameList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '[' {
		*n = NameList{}
		return nil
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return err
	}
	*n = KnownNames(names)
	return nil
}

// OpenParams carries everything the caller knows when a run opens.
// Empty strings mean "not supplied" and become "unknown".
type OpenParams struct {
	Device string
	Owner  string
	Note   string

	// ScriptPath is the resolved .star location (filesystem path or
	// "bundled:<name>"); ScriptSource is scriptlib's source tag
	// ("path", "user", "bundled"). Git provenance is computed here.
	ScriptPath   string
	ScriptSource string

	// App is caller-supplied plus adapter-probed app data.
	App AppVersion
	// DeviceOS is adapter-probed.
	DeviceOS DeviceOSVersion
}

// CloseParams carries the run outcome. Empty Passed/Outcome and an
// unknown FailureScreenshots list are recorded as "unknown".
type CloseParams struct {
	Passed             string
	Outcome            string
	FailureScreenshots NameList
}

// PassedString renders an optional bool the way manifests store it.
func PassedString(p *bool) string {
	if p == nil {
		return Unknown
	}
	if *p {
		return "true"
	}
	return "false"
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return Unknown
	}
	return strings.TrimSpace(s)
}

func (v *Versions) normalize() {
	a := &v.App
	a.Name, a.BundleID, a.Version, a.Build = orUnknown(a.Name), orUnknown(a.BundleID), orUnknown(a.Version), orUnknown(a.Build)
	a.Commit, a.Branch, a.Dirty = orUnknown(a.Commit), orUnknown(a.Branch), orUnknown(a.Dirty)
	s := &v.Spyder
	s.Version, s.Commit, s.Branch, s.Dirty = orUnknown(s.Version), orUnknown(s.Commit), orUnknown(s.Branch), orUnknown(s.Dirty)
	sc := &v.Script
	sc.Path, sc.Source, sc.Commit, sc.Branch, sc.Dirty, sc.FileDirty = orUnknown(sc.Path), orUnknown(sc.Source), orUnknown(sc.Commit), orUnknown(sc.Branch), orUnknown(sc.Dirty), orUnknown(sc.FileDirty)
	d := &v.DeviceOS
	d.Version, d.Build = orUnknown(d.Version), orUnknown(d.Build)
}

// UnknownVersions returns a Versions with every field "unknown".
func UnknownVersions() Versions {
	var v Versions
	v.normalize()
	return v
}

// mergeKnown overwrites fields of dst with fields of src that are known.
func mergeKnown(dst *string, src string) {
	if s := strings.TrimSpace(src); s != "" && s != Unknown {
		*dst = s
	}
}

// SpyderBuild returns the running binary's build stamp.
func SpyderBuild() SpyderVersion {
	v := SpyderVersion{Version: BuildVersion, Commit: BuildCommit, Branch: BuildBranch, Dirty: BuildDirty}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if v.Commit == "" {
					v.Commit = s.Value
				}
			case "vcs.modified":
				if v.Dirty == "" {
					v.Dirty = s.Value
				}
			}
		}
	}
	v.Version, v.Commit, v.Branch, v.Dirty = orUnknown(v.Version), orUnknown(v.Commit), orUnknown(v.Branch), orUnknown(v.Dirty)
	return v
}

// gitTimeout bounds each git probe so a wedged repo can't stall Open.
const gitTimeout = 3 * time.Second

func git(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// ScriptProvenance computes git provenance for a script path. Bundled
// scripts are embedded in the spyder binary, so their source version
// IS the spyder build stamp. Anything git can't answer stays unknown.
func ScriptProvenance(path, source string, spyder SpyderVersion) ScriptVersion {
	sv := ScriptVersion{Path: path, Source: source}
	if path == "" {
		return sv
	}
	if strings.HasPrefix(path, "bundled:") {
		if sv.Source == "" {
			sv.Source = "bundled"
		}
		sv.Commit, sv.Branch, sv.Dirty = spyder.Commit, spyder.Branch, spyder.Dirty
		return sv
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		sv.Path = abs
	}
	dir := filepath.Dir(sv.Path)
	if c, ok := git(dir, "rev-parse", "HEAD"); ok {
		sv.Commit = c
	}
	if b, ok := git(dir, "rev-parse", "--abbrev-ref", "HEAD"); ok {
		sv.Branch = b
	}
	if st, ok := git(dir, "status", "--porcelain", "--untracked-files=no"); ok {
		sv.Dirty = boolStr(st != "")
	}
	if st, ok := git(dir, "status", "--porcelain", "--", filepath.Base(sv.Path)); ok {
		sv.FileDirty = boolStr(st != "")
	}
	return sv
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
