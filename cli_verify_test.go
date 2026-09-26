// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/daemon"
)

func TestCLI_VerifyTwicePrintsStatusAndSkips(t *testing.T) {
	bin := buildSpyder(t)
	t.Setenv("HOME", t.TempDir())

	handler, _, _, logCap, appChan := daemon.Build(daemon.Config{Version: "test"})
	t.Cleanup(func() {
		if appChan != nil {
			appChan.Close()
		}
		if logCap != nil {
			logCap.Close()
		}
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	cwd := t.TempDir()
	marker := filepath.Join(cwd, "prep.log")
	wf := filepath.Join(cwd, "sample.yaml")
	body := `
name: cli-sample
params:
  device: handset
steps:
  - id: hello
    type: shell
    argv: ["/bin/sh", "-c", "echo prep >> ` + marker + `"]
  - id: ask
    type: human_gate
    prompt: Look
    choices:
      - id: pass
        label: Yes
`
	if err := os.WriteFile(wf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func() (string, int) {
		cmd := exec.Command(bin, "verify", wf, "--cwd", cwd, "--answer", "ask=pass")
		cmd.Env = append(os.Environ(), "SPYDER_DAEMON_URL="+ts.URL)
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("verify: %v\n%s", err, out)
			}
		}
		return string(out), code
	}

	out1, code1 := run()
	if code1 != 0 {
		t.Fatalf("first run exit %d\n%s", code1, out1)
	}
	for _, want := range []string{
		"======== STATUS passed ========",
		"workflow: cli-sample",
		"device: handset",
		"hello  ok",
		"ask  ok",
		"choice=pass",
		"======== END STATUS ========",
	} {
		if !strings.Contains(out1, want) {
			t.Errorf("first STATUS missing %q\n%s", want, out1)
		}
	}

	out2, code2 := run()
	if code2 != 0 {
		t.Fatalf("second run exit %d\n%s", code2, out2)
	}
	if !strings.Contains(out2, "======== STATUS passed ========") {
		t.Fatalf("second STATUS:\n%s", out2)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "prep") != 1 {
		t.Fatalf("prep ran %d times; second run should skip passed steps\nfirst:\n%s\nsecond:\n%s",
			strings.Count(string(b), "prep"), out1, out2)
	}
}

func buildSpyder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "spyder")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}
