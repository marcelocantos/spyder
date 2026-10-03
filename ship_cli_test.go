// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/ship"
)

// TestFastlaneCLI_GeConsumer is a CLI-handler subprocess oracle, NOT an
// installed-spyder test. The helper invokes the production argument parser
// and dispatcher, with in-process test dependencies: an empty studio envelope
// and a fake bundle. No production Keychain, signing or store API is used.
func TestFastlaneCLI_GeConsumer(t *testing.T) {
	if os.Getenv("SPYDER_FASTLANE_TEST_HELPER") == "1" {
		cwd := os.Getenv("SPYDER_FASTLANE_TEST_DIR")
		ship.AuditRoot = func() string { return filepath.Join(cwd, "audit") }
		// Only this go-test subprocess installs synthetic dependencies. The
		// production runFastlane path passes RequireSecretsAccess and RunFastlane.
		runFastlaneWith(os.Args[3:], func() error { return nil }, func(opts ship.FastlaneOpts) (int, error) {
			opts.Cwd = cwd
			opts.LookPath = func(file string) (string, error) {
				if file != "bundle" {
					return "", fmt.Errorf("unexpected lookup %q", file)
				}
				return filepath.Join(cwd, "bundle"), nil
			}
			opts.LoadEnvelope = func(studio string) (*ship.Envelope, error) {
				if studio != ship.StudioSquz {
					return nil, fmt.Errorf("unexpected studio %q", studio)
				}
				return &ship.Envelope{Version: ship.EnvelopeVersion}, nil
			}
			opts.Environ = []string{"PATH=/usr/bin:/bin", "HOME=" + cwd}
			return ship.RunFastlane(opts)
		})
		return
	}

	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatalf("read Go test executable identity: %v", err)
	}
	build, _ := debug.ReadBuildInfo()
	if build == nil {
		t.Fatal("Go test executable lacks build info")
	}
	t.Logf("Go test executable (not installed spyder): path=%s sha256=%x main=%s version=%s", os.Args[0], sha256.Sum256(binary), build.Main.Path, build.Main.Version)

	for _, tc := range []struct {
		name      string
		lane      []string
		needsGate bool
	}{
		{"submit", []string{"submit_release", "project:Game.xcodeproj"}, true},
		{"platform-submit", []string{"ios", "submit_release", "project:Game.xcodeproj"}, true},
		{"external", []string{"upload_testflight", "project:Game.xcodeproj", "external:true"}, true},
		{"platform-external", []string{"ios", "upload_testflight", "project:Game.xcodeproj", "--external=true"}, true},
		{"internal", []string{"upload_testflight", "project:Game.xcodeproj", "external:false"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			marker := filepath.Join(cwd, "launched")
			bundle := filepath.Join(cwd, "bundle")
			// The fake records only argv, never makes a network or store call.
			if err := os.WriteFile(bundle, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > launched\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			invoke := func(flags ...string) (string, error) {
				t.Helper()
				argv := append([]string{"-test.run=^TestFastlaneCLI_GeConsumer$", "--", "--studio", "squz"}, flags...)
				argv = append(argv, "--")
				argv = append(argv, tc.lane...)
				cmd := exec.Command(os.Args[0], argv...)
				cmd.Dir = cwd
				cmd.Env = append(os.Environ(), "SPYDER_FASTLANE_TEST_HELPER=1", "SPYDER_FASTLANE_TEST_DIR="+cwd, "HOME="+cwd)
				out, err := cmd.CombinedOutput()
				return string(out), err
			}
			if tc.needsGate {
				out, err := invoke()
				if err == nil || !strings.Contains(out, "--confirm") {
					t.Fatalf("unconfirmed publish was not denied: err=%v output=%s", err, out)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("fake child ran before confirmation: %v", err)
				}
			}
			out, err := invoke("--dry-run")
			if err != nil || !strings.Contains(out, "dry-run:") {
				t.Fatalf("dry-run failed: err=%v output=%s", err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("fake child ran on dry-run: %v", err)
			}
			out, err = invoke("--confirm")
			if err != nil {
				t.Fatalf("confirmed fake child failed: err=%v output=%s", err, out)
			}
			body, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("fake child never ran: %v", err)
			}
			want := append([]string{"exec", "fastlane"}, tc.lane...)
			if got := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n"); !reflect.DeepEqual(got, want) {
				t.Fatalf("forwarded argv = %q; want %q", got, want)
			}
		})
	}
}
