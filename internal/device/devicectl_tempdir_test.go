// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoveStaleDevicectlTempDirs(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "spyder-devctl-old")
	conn := filepath.Join(dir, "spyder-devctl-conn-old")
	fresh := filepath.Join(dir, "spyder-devctl-fresh")
	other := filepath.Join(dir, "unrelated-old")
	for _, p := range []string{old, conn, fresh, other} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	for _, p := range []string{old, conn, other} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if got := RemoveStaleDevicectlTempDirs(dir, 10*time.Minute); got != 2 {
		t.Fatalf("removed %d, want 2", got)
	}
	for p, want := range map[string]bool{old: false, conn: false, fresh: true, other: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}
