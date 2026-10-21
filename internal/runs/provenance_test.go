// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitRepoWithScript(t *testing.T) (dir, script, commit string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "smoke-branch")
	script = filepath.Join(dir, "euan_shop_smoke.star")
	_ = os.WriteFile(script, []byte("emit(1)\n"), 0o600)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir, script, run("rev-parse", "HEAD")
}

func TestOpenWith_RecordsSourceVersions(t *testing.T) {
	_, script, commit := gitRepoWithScript(t)
	oldB, oldC := BuildBranch, BuildCommit
	BuildBranch, BuildCommit = "runs-archive", "deadbeef"
	t.Cleanup(func() { BuildBranch, BuildCommit = oldB, oldC })

	now := time.Now()
	s := newTestStore(t, &now)
	r, err := s.OpenWith(OpenParams{
		Device: "A23", Owner: "euan-shop", ScriptPath: script, ScriptSource: "path",
		App:      AppVersion{BundleID: "com.example.shop", Version: "1.2", Build: "12", Commit: "c0ffee", Branch: "feature", Dirty: "true"},
		DeviceOS: DeviceOSVersion{Version: "14", Build: "UP1A"},
	})
	if err != nil {
		t.Fatal(err)
	}
	v := r.Versions
	if v.Script.Commit != commit || v.Script.Branch != "smoke-branch" || v.Script.Dirty != "false" || v.Script.FileDirty != "false" {
		t.Errorf("script = %+v (want commit %s)", v.Script, commit)
	}
	if v.Spyder.Commit != "deadbeef" || v.Spyder.Branch != "runs-archive" {
		t.Errorf("spyder = %+v", v.Spyder)
	}
	if v.App.Name != Unknown || v.App.Version != "1.2" || v.App.Commit != "c0ffee" || v.App.Dirty != "true" {
		t.Errorf("app = %+v", v.App)
	}
	if v.DeviceOS != (DeviceOSVersion{"14", "UP1A"}) {
		t.Errorf("device_os = %+v", v.DeviceOS)
	}
	// Persisted, not just in memory.
	got, _ := s.Get(r.ID)
	if got.Versions == nil || *got.Versions != *v {
		t.Errorf("manifest versions = %+v", got.Versions)
	}
}

func TestScriptProvenance_DirtyScript(t *testing.T) {
	_, script, _ := gitRepoWithScript(t)
	_ = os.WriteFile(script, []byte("emit(2)\n"), 0o600)
	sv := ScriptProvenance(script, "path", SpyderBuild())
	if sv.Dirty != "true" || sv.FileDirty != "true" {
		t.Errorf("dirty script = %+v", sv)
	}
}

func TestScriptProvenance_OutsideGitIsUnknown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.star")
	_ = os.WriteFile(p, []byte("1\n"), 0o600)
	v := Versions{Script: ScriptProvenance(p, "path", SpyderBuild())}
	v.normalize()
	if v.Script.Commit != Unknown || v.Script.Branch != Unknown || v.Script.Dirty != Unknown {
		t.Errorf("non-git script = %+v", v.Script)
	}
}

func TestScriptProvenance_BundledUsesSpyderBuild(t *testing.T) {
	sp := SpyderVersion{Version: "v1", Commit: "abc", Branch: "master", Dirty: "false"}
	sv := ScriptProvenance("bundled:skeleton", "bundled", sp)
	if sv.Commit != "abc" || sv.Branch != "master" || sv.Dirty != "false" {
		t.Errorf("bundled = %+v", sv)
	}
}

func TestOpen_UnavailableFieldsAreUnknown(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	r, _ := s.Open("iPad", "o", "")
	_ = s.Close(r.ID)
	b, _ := os.ReadFile(filepath.Join(s.BaseDir(), r.ID, manifestName))
	var m struct {
		Versions map[string]map[string]string `json:"versions"`
		Result   map[string]any               `json:"result"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for sect, fields := range m.Versions {
		for k, val := range fields {
			if val == "" {
				t.Errorf("versions.%s.%s is empty; want a value or %q", sect, k, Unknown)
			}
			if sect != "spyder" && val != Unknown {
				t.Errorf("versions.%s.%s = %q; nothing supplied so want %q", sect, k, val, Unknown)
			}
		}
	}
	for _, k := range []string{"passed", "outcome", "failure_screenshots"} {
		if m.Result[k] != Unknown {
			t.Errorf("result.%s = %v; want unknown", k, m.Result[k])
		}
	}
}

func TestCloseWith_RecordsOutcome(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	r, _ := s.Open("iPad", "o", "")
	if err := s.CloseWith(r.ID, CloseParams{Passed: "true", Outcome: "all laps ok", FailureScreenshots: KnownNames(nil)}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	if got.Result.Passed != "true" || got.Result.Outcome != "all laps ok" || !got.Result.FailureScreenshots.Known || len(got.Result.FailureScreenshots.Names) != 0 {
		t.Errorf("result = %+v", got.Result)
	}
	b, _ := os.ReadFile(filepath.Join(s.BaseDir(), r.ID, manifestName))
	if !strings.Contains(string(b), `"failure_screenshots": []`) {
		t.Errorf("known-empty list not written as []: %s", b)
	}
}

func TestUpdateProvenance_OnlyKnownValuesOverwrite(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	r, _ := s.OpenWith(OpenParams{Device: "A", Owner: "o", App: AppVersion{Version: "1.0", Commit: "aaa"}})
	if err := s.UpdateProvenance(r.ID, OpenParams{App: AppVersion{Commit: "bbb", Version: Unknown}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	if got.Versions.App.Commit != "bbb" || got.Versions.App.Version != "1.0" {
		t.Errorf("app = %+v", got.Versions.App)
	}
}

func TestNameList_JSONRoundTrip(t *testing.T) {
	for _, n := range []NameList{{}, KnownNames(nil), KnownNames([]string{"a.png"})} {
		b, _ := json.Marshal(n)
		var back NameList
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if back.Known != n.Known || len(back.Names) != len(n.Names) {
			t.Errorf("%s round-tripped to %+v", b, back)
		}
	}
}
