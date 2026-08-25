// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareFastlane_DryRunEnvIsolation(t *testing.T) {
	env := &Envelope{
		Version:       EnvelopeVersion,
		MatchPassword: "super-secret-match",
		ASC: &ASCCreds{
			IssuerID: "11111111-2222-3333-4444-555555555555",
			KeyID:    "ABCDEF1234",
			P8:       "-----BEGIN PRIVATE KEY-----\nMII\n-----END PRIVATE KEY-----\n",
		},
		PlayServiceAccount: json.RawMessage(`{"type":"service_account","client_email":"sa@x.iam.gserviceaccount.com","private_key":"x"}`),
	}
	fakeBundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.WriteFile(fakeBundle, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	parent := []string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
		"MATCH_PASSWORD=", // empty OK
	}
	plan, err := prepareFastlaneFromEnv(StudioSquz, env, FastlaneOpts{
		Studio:   StudioSquz,
		Args:     []string{"pilot", "--ipa", "app.ipa"},
		DryRun:   true,
		LookPath: func(string) (string, error) { return fakeBundle, nil },
		Environ:  parent,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer WipeTemps(plan.TempFiles)

	if plan.Class != ClassTestPublish {
		t.Fatalf("class %s", plan.Class)
	}
	sum := plan.RedactedSummary()
	if strings.Contains(sum, "super-secret-match") || strings.Contains(sum, "BEGIN PRIVATE") {
		t.Fatalf("redacted summary leaked secrets: %s", sum)
	}
	hasMatch := false
	for _, kv := range plan.ChildEnv {
		if strings.HasPrefix(kv, "MATCH_PASSWORD=") {
			hasMatch = true
			if kv != "MATCH_PASSWORD=super-secret-match" {
				t.Fatalf("child match: %s", kv)
			}
		}
		if strings.Contains(kv, "BEGIN PRIVATE") {
			t.Fatal("PEM must be in temp file, not env value of other keys incorrectly")
		}
	}
	if !hasMatch {
		t.Fatal("child missing MATCH_PASSWORD")
	}
	// Parent must not gain MATCH_PASSWORD — we only built child env.
	for _, kv := range parent {
		if strings.HasPrefix(kv, "MATCH_PASSWORD=") && kv != "MATCH_PASSWORD=" {
			t.Fatal("parent polluted")
		}
	}

	var p8 string
	for _, kv := range plan.ChildEnv {
		if strings.HasPrefix(kv, "APP_STORE_CONNECT_API_KEY_KEY_PATH=") {
			p8 = strings.TrimPrefix(kv, "APP_STORE_CONNECT_API_KEY_KEY_PATH=")
		}
	}
	if p8 == "" {
		t.Fatal("missing p8 path")
	}
	fi, err := os.Stat(p8)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("p8 mode %v not 0600", fi.Mode())
	}
	WipeTemps(plan.TempFiles)
	if _, err := os.Stat(p8); !os.IsNotExist(err) {
		t.Fatalf("temp p8 still exists after wipe: %v", err)
	}
}

func TestPrepareFastlane_RefuseParentMatchPassword(t *testing.T) {
	fakeBundle := filepath.Join(t.TempDir(), "bundle")
	_ = os.WriteFile(fakeBundle, []byte("#!/bin/sh\n"), 0o755)
	_, err := prepareFastlaneFromEnv(StudioSquz, &Envelope{Version: 1}, FastlaneOpts{
		Args:     []string{"sync_certs"},
		LookPath: func(string) (string, error) { return fakeBundle, nil },
		Environ:  []string{"PATH=/bin", "MATCH_PASSWORD=leaked"},
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "MATCH_PASSWORD") {
		t.Fatalf("want parent MATCH_PASSWORD error, got %v", err)
	}
}

func TestPrepareFastlane_ConfirmGate(t *testing.T) {
	fakeBundle := filepath.Join(t.TempDir(), "bundle")
	_ = os.WriteFile(fakeBundle, []byte("#!/bin/sh\n"), 0o755)
	_, err := prepareFastlaneFromEnv(StudioSquz, &Envelope{Version: 1}, FastlaneOpts{
		Args:     []string{"deliver", "--submit_for_review"},
		LookPath: func(string) (string, error) { return fakeBundle, nil },
		Environ:  []string{"PATH=/bin"},
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("want confirm error, got %v", err)
	}
	// dry-run bypasses confirm
	plan, err := prepareFastlaneFromEnv(StudioSquz, &Envelope{Version: 1}, FastlaneOpts{
		Args:     []string{"deliver", "--submit_for_review"},
		DryRun:   true,
		LookPath: func(string) (string, error) { return fakeBundle, nil },
		Environ:  []string{"PATH=/bin"},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	WipeTemps(plan.TempFiles)
}

func TestRunFastlane_AuditOnPublish(t *testing.T) {
	auditDir := t.TempDir()
	prev := AuditRoot
	AuditRoot = func() string { return auditDir }
	t.Cleanup(func() { AuditRoot = prev })

	fakeBundle := filepath.Join(t.TempDir(), "bundle")
	script := "#!/bin/sh\necho MATCH_PASSWORD=should-scrub\necho done\n"
	if err := os.WriteFile(fakeBundle, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	plan, err := prepareFastlaneFromEnv(StudioSquz, &Envelope{Version: 1, MatchPassword: "x"}, FastlaneOpts{
		Studio:   StudioSquz,
		Args:     []string{"pilot", "--ipa", "x.ipa"},
		Cwd:      cwd,
		LookPath: func(string) (string, error) { return fakeBundle, nil },
		Environ:  []string{"PATH=/bin", "HOME=" + cwd},
	}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer WipeTemps(plan.TempFiles)

	code, err := executeFastlanePlan(plan, FastlaneOpts{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	ents, err := os.ReadDir(filepath.Join(auditDir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	var sawReflect, sawLog bool
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), "-reflect.md") {
			sawReflect = true
		}
		if strings.HasSuffix(e.Name(), ".log") {
			sawLog = true
			raw, _ := os.ReadFile(filepath.Join(auditDir, "runs", e.Name()))
			if strings.Contains(string(raw), "should-scrub") {
				t.Fatalf("log not scrubbed: %s", raw)
			}
		}
	}
	if !sawReflect || !sawLog {
		t.Fatalf("want reflect+log, got %v", ents)
	}
	days, _ := filepath.Glob(filepath.Join(auditDir, "*.jsonl"))
	if len(days) != 1 {
		t.Fatalf("jsonl files: %v", days)
	}
}

func TestCheckStudioMatchfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "fastlane"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "git_url(\"git@github.com:squz/certs.git\")\nteam_id(\"SWA3H3N7TW\")\n"
	if err := os.WriteFile(filepath.Join(dir, "fastlane", "Matchfile"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckStudioMatchfile(StudioSquz, dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckStudioMatchfile(StudioMinicades, dir); err == nil {
		t.Fatal("expected team mismatch")
	}
}
