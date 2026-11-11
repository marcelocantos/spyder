// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package exitlog

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/paths"
)

func TestRecordAppends(t *testing.T) {
	t.Setenv(paths.DataDirEnv, t.TempDir())
	Record(1, "self-restart: dispatch stuck\nafter deadline")
	Record(1, "serve: http listen")
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "status=1 reason=self-restart: dispatch stuck after deadline") {
		t.Fatalf("exit log:\n%s", b)
	}
}

func TestTrimKeepsNewestLines(t *testing.T) {
	t.Setenv(paths.DataDirEnv, t.TempDir())
	if err := os.MkdirAll(paths.Base(), 0o755); err != nil {
		t.Fatal(err)
	}
	old := strings.Repeat("old line\n", maxBytes/9+10)
	if err := os.WriteFile(Path(), []byte(old+"newest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trim()
	b, _ := os.ReadFile(Path())
	if len(b) > maxBytes/2 || !strings.HasSuffix(string(b), "newest\n") || !strings.HasPrefix(string(b), "old line\n") {
		t.Fatalf("trim kept %d bytes, suffix ok=%v", len(b), strings.HasSuffix(string(b), "newest\n"))
	}
}

// A real panic in a child process must land in the exit log.
func TestCrashOutputCapturesPanic(t *testing.T) {
	if os.Getenv("EXITLOG_CRASH_CHILD") == "1" {
		if err := InstallCrashOutput("test"); err != nil {
			t.Fatal(err)
		}
		panic("planted crash for exitlog")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashOutputCapturesPanic$")
	cmd.Env = append(os.Environ(), "EXITLOG_CRASH_CHILD=1", paths.DataDirEnv+"="+dir)
	if err := cmd.Run(); err == nil {
		t.Fatal("child did not crash")
	}
	t.Setenv(paths.DataDirEnv, dir)
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "started version=test") || !strings.Contains(string(b), "panic: planted crash for exitlog") {
		t.Fatalf("exit log missing the panic:\n%s", b)
	}
}
