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

func TestCLIDevPolicySetFileShowClear(t *testing.T) {
	bin := buildSpyder(t)
	t.Setenv("HOME", t.TempDir())
	handler, _, _, logs, apps := daemon.Build(daemon.Config{Version: "test"})
	t.Cleanup(func() {
		if apps != nil {
			apps.Close()
		}
		if logs != nil {
			logs.Close()
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	profile := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(profile, []byte(`{"version":1,"global":{"orientation":"landscapeRight"},"groups":[],"apps":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "SPYDER_DAEMON_URL="+server.URL)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := run("dev-policy", "set", profile, "--as", "office"); !strings.Contains(out, "landscapeRight") {
		t.Fatal(out)
	}
	if out := run("dev-policy", "show", "--bundle-id", "com.minicades.stockcars"); !strings.Contains(out, "landscapeRight") || !strings.Contains(out, "office") {
		t.Fatal(out)
	}
	if out := run("dev-policy", "clear", "--as", "office"); strings.Contains(out, "landscapeRight") {
		t.Fatal("clear retained flags", out)
	}
}
