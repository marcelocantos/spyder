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

func TestWriteAudit_ReflectForPublish(t *testing.T) {
	dir := t.TempDir()
	prev := AuditRoot
	AuditRoot = func() string { return dir }
	t.Cleanup(func() { AuditRoot = prev })

	rec := &AuditRecord{
		Studio:        StudioSquz,
		Class:         ClassTestPublish,
		Action:        "pilot",
		Argv:          []string{"bundle", "exec", "fastlane", "pilot"},
		ExitCode:      0,
		DurationMS:    12,
		DryRun:        false,
		SpyderVersion: "test",
		SecretsPresent: map[string]bool{
			KindMatchPassword: true,
		},
		Fingerprints: map[string]string{"asc_key_id": "ABC"},
	}
	if err := WriteAudit(rec); err != nil {
		t.Fatal(err)
	}
	if rec.ReflectPath == "" {
		t.Fatal("expected reflect path")
	}
	if _, err := os.Stat(rec.ReflectPath); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(rec.ReflectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Lane class: test_publish") {
		t.Fatalf("stub: %s", body)
	}
	day := rec.Timestamp.UTC().Format("2006-01-02")
	raw, err := os.ReadFile(filepath.Join(dir, day+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var got AuditRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ReflectPath == "" || got.Class != ClassTestPublish {
		t.Fatalf("jsonl: %+v", got)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatal("leaked")
	}
}

func TestWriteAudit_NoReflectDryRunOrRead(t *testing.T) {
	dir := t.TempDir()
	prev := AuditRoot
	AuditRoot = func() string { return dir }
	t.Cleanup(func() { AuditRoot = prev })

	for _, rec := range []*AuditRecord{
		{Class: ClassTestPublish, Action: "pilot", DryRun: true, Argv: []string{"pilot"}},
		{Class: ClassRead, Action: "sync_certs", DryRun: false, Argv: []string{"sync_certs"}},
	} {
		rec.Studio = StudioSquz
		if err := WriteAudit(rec); err != nil {
			t.Fatal(err)
		}
		if rec.ReflectPath != "" {
			t.Fatalf("unexpected reflect for %+v", rec)
		}
		ents, _ := os.ReadDir(filepath.Join(dir, "runs"))
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), "-reflect.md") {
				t.Fatalf("reflect file written for dry-run/read: %s", e.Name())
			}
		}
	}
}

func TestListUnfilledReflections(t *testing.T) {
	dir := t.TempDir()
	prev := AuditRoot
	AuditRoot = func() string { return dir }
	t.Cleanup(func() { AuditRoot = prev })

	rec := &AuditRecord{
		Studio: StudioSquz, Class: ClassProdPublish, Action: "deliver",
		Argv: []string{"deliver"}, SpyderVersion: "t",
	}
	if err := WriteAudit(rec); err != nil {
		t.Fatal(err)
	}
	got, err := ListUnfilledReflections()
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v err %v", got, err)
	}
	// Mark filled
	body, _ := os.ReadFile(got[0])
	filled := strings.Replace(string(body),
		"What happened (fill in):\nSurprises:",
		"What happened (fill in):\nshipped OK\nSurprises:", 1)
	_ = os.WriteFile(got[0], []byte(filled), 0o600)
	got, err = ListUnfilledReflections()
	if err != nil || len(got) != 0 {
		t.Fatalf("after fill: %v %v", got, err)
	}
}
